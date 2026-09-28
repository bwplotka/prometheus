// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package histogramconv

import (
	"context"
	"slices"

	"github.com/prometheus/common/model"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
)

// NewQuerier returns a querier that wraps the given querier and converts
// between histogram representations in Select, from the representations in
// convertFrom:
//
//   - A selector for classic histogram series, i.e. with a metric name with a
//     _bucket, _count or _sum suffix, also returns the classic histogram series
//     converted from the native histograms of the base name, from NHCB and
//     NHE.
//   - A selector for any other metric name also returns the NHCB assembled
//     from the classic histogram series of that name, from Classic.
//
// Each selector is handled by exactly one of the two, and both read stored
// series only, so nothing is converted twice and all representations can be
// converted from at the same time.
//
// Where a selector reads a histogram both stored and converted, stored data
// wins: converted samples are dropped at the timestamps of the stored samples
// of the same histogram that the selector reads, and the remaining ones are
// merged into the stored series with the same labels.
//
// Matchers on the ConvertStoredAsLabel override convertFrom per selector, and
// select the representations of the stored samples to return, too. Matchers
// on it that match the empty value, but none of the representations, e.g.
// __convert_stored_as__="", turn the conversion off for the selector, which
// then returns the stored series as they are. Matchers on the
// DebugStoredAsLabel that match "true" add the StoredAsLabel to the returned
// series. Both are removed before selecting from the wrapped querier.
//
// Known limitations:
//
//   - Only Select converts, and only the PromQL engine uses the querier, see
//     promql.EngineOpts.HistogramConversionFrom. LabelNames, LabelValues and
//     other APIs, e.g. the remote read endpoint, return stored data only.
//   - Stored data only wins at the exact timestamps of the stored samples the
//     selector reads. Where a histogram is stored in both representations at
//     different timestamps, e.g. ingested from different sources, the merged
//     series alternates between them, and a selector with le matchers that do
//     not match any stored bucket returns the matching converted buckets.
//   - Converted samples, and stored samples filtered by representation, are
//     buffered one histogram at a time.
func NewQuerier(q storage.Querier, convertFrom []Representation) storage.Querier {
	return &querier{Querier: q, convertFrom: newRepresentations(convertFrom...)}
}

type querier struct {
	storage.Querier

	convertFrom representations
}

// Select implements the storage.Querier interface. It selects all the series
// it might need from the wrapped querier before it returns, and reads them on
// the first call of Next of the returned series set, as queriers that merge
// remote read storage panic if Select is called after that, see
// storage.NewMergeQuerier.
func (q *querier) Select(ctx context.Context, sortSeries bool, hints *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	sel, err := newSelector(matchers, q.convertFrom)
	switch {
	case err != nil:
		return storage.ErrSeriesSet(err)
	case sel.stored == 0:
		return storage.EmptySeriesSet()
	case sel.passThrough():
		return q.Querier.Select(ctx, sortSeries, hints, sel.matchers...)
	}
	s := &seriesSet{
		sel:        sel,
		sortSeries: sortSeries,
		storedSet:  q.Querier.Select(ctx, false, hints, sel.matchers...),
	}
	if len(sel.sourceMatchers) > 0 {
		s.sourceSet = q.Querier.Select(ctx, false, hints, sel.sourceMatchers...)
	}
	return s
}

// series is a series of a materialized histogramGroup: a converted series, a
// stored series whose samples are filtered, or a stored series returned
// unchanged, whose samples are only read if needed.
type series struct {
	lset    labels.Labels
	samples samples
	floats  []fSample
	inUse   int
	// stored is the stored series returned unchanged, nil once its samples
	// are read into samples.
	stored storage.Series
}

// iterator returns a chunkenc.Iterator over the samples of s.
func (s *series) iterator(it chunkenc.Iterator) chunkenc.Iterator {
	if s.stored != nil {
		return s.stored.Iterator(it)
	}
	if len(s.floats) > 0 {
		s.inUse++
		if fsi, ok := it.(*floatSeriesIterator); ok {
			return fsi.reset(s, s.floats)
		}
		return (&floatSeriesIterator{}).reset(s, s.floats)
	}
	return storage.NewListSeriesIterator(s.samples)
}

// lazySeries is a storage.Series whose histogramGroup is materialized on
// demand when Iterator is called.
type lazySeries struct {
	s     *seriesSet
	group *histogramGroup
	idx   int
	lset  labels.Labels
}

func (l lazySeries) Labels() labels.Labels { return l.lset }

func (l lazySeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	if fsi, ok := it.(*floatSeriesIterator); ok {
		fsi.release()
	}
	if l.s.cachedGroup != l.group {
		if err := l.s.materialize(l.group, false); err != nil {
			return errIterator{err: err}
		}
	}
	return l.s.cachedSeries[l.idx].iterator(it)
}

// seriesSet returns the stored and the converted series of a selector, one
// histogramGroup at a time, and sorted by labels if sortSeries is true.
type seriesSet struct {
	sel        selector
	sortSeries bool
	// storedSet holds the stored series the selector reads, and also the
	// series to convert from if sourceSet is nil.
	storedSet, sourceSet storage.SeriesSet

	// it, h and fh are reused to read the samples of stored series.
	it chunkenc.Iterator
	h  *histogram.Histogram
	fh *histogram.FloatHistogram

	classicBuilder *classicSeriesBuilder
	boundaries     []float64

	initialized bool
	groups      []*histogramGroup
	groupIdx    int

	current []storage.Series
	idx     int

	cachedGroup  *histogramGroup
	cachedSeries []*series

	err      error
	warnings annotations.Annotations
}

func (s *seriesSet) Next() bool {
	if !s.initialized {
		s.initialized = true
		if s.err = s.init(); s.err != nil {
			return false
		}
	}
	for s.idx >= len(s.current) {
		if s.groupIdx >= len(s.groups) {
			return false
		}
		g := s.groups[s.groupIdx]
		s.groupIdx++
		s.current = s.current[:0]
		s.idx = 0
		if s.err = s.appendGroupSeries(g); s.err != nil {
			return false
		}
	}
	s.idx++
	return true
}

func (s *seriesSet) At() storage.Series {
	if s.idx == 0 || s.idx > len(s.current) {
		return nil
	}
	return s.current[s.idx-1]
}

func (s *seriesSet) Err() error { return s.err }

func (s *seriesSet) Warnings() annotations.Annotations { return s.warnings }

// init drains storedSet and sourceSet, groups their series by histogram, and
// computes the shared exponential bucket boundaries if needed.
func (s *seriesSet) init() error {
	idx := newHistogramIndex()
	ignoreLe := s.sel.suffix != ""
	for s.storedSet.Next() {
		ser := s.storedSet.At()
		lset := ser.Labels()
		if s.sel.from != 0 && lset.Get(model.MetricNameLabel) != s.sel.name {
			g := idx.get(lset, true)
			g.sources = append(g.sources, ser)
			continue
		}
		if s.sel.suffix == "" && !matches(lset.Get(labels.BucketLabel), s.sel.leMatchers) {
			continue
		}
		g := idx.get(lset, ignoreLe)
		g.stored = append(g.stored, ser)
	}
	s.warnings.Merge(s.storedSet.Warnings())
	if err := s.storedSet.Err(); err != nil {
		return err
	}
	if s.sourceSet != nil {
		for s.sourceSet.Next() {
			ser := s.sourceSet.At()
			g := idx.get(ser.Labels(), true)
			g.sources = append(g.sources, ser)
		}
		s.warnings.Merge(s.sourceSet.Warnings())
		if err := s.sourceSet.Err(); err != nil {
			return err
		}
	}
	s.groups = idx.groups

	if s.sel.from.has(NHE) && s.sel.suffix == histogram.ClassicSuffixBucket {
		var err error
		if s.boundaries, err = s.exponentialBoundaries(); err != nil {
			return err
		}
	}

	if s.sortSeries {
		for _, g := range s.groups {
			if err := s.appendGroupSeries(g); err != nil {
				return err
			}
		}
		s.groupIdx = len(s.groups)
		slices.SortFunc(s.current, func(a, b storage.Series) int {
			return labels.Compare(a.Labels(), b.Labels())
		})
	}
	return nil
}

// appendGroupSeries appends the storage.Series of g to s.current. If g only
// has stored series that need no representation filtering, they are appended
// without reading their samples.
func (s *seriesSet) appendGroupSeries(g *histogramGroup) error {
	filter := s.sel.stored != allRepresentations || s.sel.debug
	if len(g.sources) == 0 && !filter {
		start := len(s.current)
		s.current = append(s.current, g.stored...)
		slices.SortFunc(s.current[start:], func(a, b storage.Series) int {
			return labels.Compare(a.Labels(), b.Labels())
		})
		return nil
	}
	if err := s.materialize(g, true); err != nil {
		return err
	}
	for i, ser := range s.cachedSeries {
		if ser.stored != nil {
			s.current = append(s.current, ser.stored)
		} else {
			s.current = append(s.current, lazySeries{s: s, group: g, idx: i, lset: ser.lset})
		}
	}
	return nil
}

// materialize converts and merges the series of g into s.cachedSeries.
func (s *seriesSet) materialize(g *histogramGroup, collectWarnings bool) error {
	var (
		stored []*series
		filter = s.sel.stored != allRepresentations || s.sel.debug
		split  = storedSplitter{stored: s.sel.stored, debug: s.sel.debug}
	)
	for _, ser := range g.stored {
		if !filter {
			stored = append(stored, &series{lset: ser.Labels(), stored: ser})
			continue
		}
		parts, err := split.split(ser)
		if err != nil {
			return err
		}
		stored = append(stored, parts...)
	}

	var (
		converted []*series
		err       error
	)
	if len(g.sources) > 0 {
		if s.sel.suffix != "" {
			converted, err = s.toClassic(g.sources, collectWarnings)
		} else {
			converted, err = s.toNHCB(g.sources, collectWarnings)
		}
		if err != nil {
			return err
		}
	}
	if len(stored) > 0 && len(converted) > 0 {
		if converted, err = s.storedWins(stored, converted); err != nil {
			return err
		}
	}

	out := s.cachedSeries[:0]
	out = append(out, stored...)
	out = append(out, converted...)
	slices.SortFunc(out, func(a, b *series) int {
		return labels.Compare(a.lset, b.lset)
	})
	s.cachedGroup = g
	s.cachedSeries = out
	return nil
}

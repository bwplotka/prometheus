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
	"math"
	"slices"

	"github.com/prometheus/common/model"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
)

// toClassic converts the native histograms of sources, of the representations
// in s.sel.from, to the classic histogram series with s.sel.suffix, and
// returns the converted series whose le label matches all s.sel.leMatchers. In
// debug mode, the converted series have the StoredAsLabel, set to the
// representation of the native histograms they were converted from. If
// collectWarnings is true, an invalid native histogram, e.g. with fewer
// buckets than its spans need, is reported in s.warnings.
//
// Native histograms with an exponential schema have no fixed bucket
// boundaries. So that the resulting classic histograms can be aggregated by
// le, across series and over time, all of them are converted with the same
// derived boundaries in s.boundaries, see exponentialBoundaries. Hence the le
// set depends on the selected series and time range, a single low resolution
// histogram lowers the resolution of all of them, and histograms with many
// buckets result in many classic series.
//
// A converted series is marked stale at the first sample of its native
// histogram that does not result in it anymore, e.g. because the native
// histogram went stale, its bucket layout changed or it is not converted, just
// like the scrape loop marks series stale that disappear from a target.
// Converted samples other than staleness markers have the start timestamp of
// the native histogram sample they were converted from.
func (s *seriesSet) toClassic(sources []storage.Series, collectWarnings bool) ([]*series, error) {
	b := s.classicBuilder
	if b == nil {
		b = newClassicSeriesBuilder()
		s.classicBuilder = b
	} else {
		b.reset()
	}

	for _, ser := range sources {
		lset := ser.Labels()
		// A cache drops its label sets when the labels change, so debug
		// mode, which adds the representation to the labels, uses one per
		// representation.
		var (
			nhcbLabels, nheLabels = lset, lset
			nhcbCache             = &b.nhcbCache
			nheCache              = nhcbCache
		)
		if s.sel.debug {
			nhcbLabels, nheLabels = withStoredAs(lset, NHCB), withStoredAs(lset, NHE)
			nheCache = &b.nheCache
		}
		b.startSeries()
		s.it = ser.Iterator(s.it)
		for valType := s.it.Next(); valType != chunkenc.ValNone; valType = s.it.Next() {
			b.startSample(s.it.AtST(), s.it.AtT())
			if valType == chunkenc.ValHistogram || valType == chunkenc.ValFloatHistogram {
				// This works for histograms with integer counts, too.
				if _, s.fh = s.it.AtFloatHistogram(s.fh); convertible(s.fh, s.sel.from) {
					if err := s.fh.Validate(); err != nil {
						if collectWarnings {
							s.warnings.Add(annotations.NewNativeToClassicConversionWarning(lset.Get(model.MetricNameLabel), err))
						}
					} else {
						var err error
						if histogram.IsExponentialSchema(s.fh.Schema) {
							err = histogram.ConvertExponentialToClassic(s.fh, s.boundaries, nheLabels, b.lsetBuilder, s.sel.suffix, nheCache, b.emit)
						} else {
							err = histogram.ConvertNHCBToClassic(s.fh, nhcbLabels, b.lsetBuilder, s.sel.suffix, nhcbCache, b.emit)
						}
						if err != nil {
							return nil, err
						}
					}
				}
			}
			b.endSample()
		}
		if err := s.it.Err(); err != nil {
			return nil, err
		}
	}

	converted := make([]*series, 0, len(b.series))
	for _, ser := range b.series {
		if matches(ser.lset.Get(labels.BucketLabel), s.sel.leMatchers) {
			converted = append(converted, ser)
		}
	}
	return converted, nil
}

// convertible reports whether the native histogram fh, of the representations
// in from, is converted to classic histogram series.
func convertible(fh *histogram.FloatHistogram, from representations) bool {
	// Staleness markers are not converted, whatever their schema. The series
	// converted from the previous sample are marked stale instead.
	if fh == nil || value.IsStaleNaN(fh.Sum) {
		return false
	}
	return (from.has(NHCB) && histogram.IsCustomBucketsSchema(fh.Schema)) ||
		(from.has(NHE) && histogram.IsExponentialSchema(fh.Schema))
}

// exponentialBoundaries returns the le boundaries to convert the exponential
// native histograms amongst s.groups with: the union of the boundaries of all
// of them, see histogram.AppendClassicBoundaries, reduced to the lowest schema
// amongst them. As the boundaries of a lower schema are boundaries of every
// higher schema, too, the conversion is exact for each histogram. As all of
// them are converted with the same boundaries, the resulting classic
// histograms can be aggregated by le, across series and over time.
func (s *seriesSet) exponentialBoundaries() ([]float64, error) {
	minSchema := int32(math.MaxInt32)
	for _, g := range s.groups {
		for _, ser := range g.sources {
			s.it = ser.Iterator(s.it)
			for valType := s.it.Next(); valType != chunkenc.ValNone; valType = s.it.Next() {
				if valType != chunkenc.ValHistogram && valType != chunkenc.ValFloatHistogram {
					continue
				}
				_, s.fh = s.it.AtFloatHistogram(s.fh)
				if histogram.IsExponentialSchema(s.fh.Schema) && convertible(s.fh, s.sel.from) && s.fh.Validate() == nil {
					minSchema = min(minSchema, s.fh.Schema)
				}
			}
			if err := s.it.Err(); err != nil {
				return nil, err
			}
		}
	}
	if minSchema == math.MaxInt32 {
		return nil, nil
	}

	var boundaries []float64
	for _, g := range s.groups {
		for _, ser := range g.sources {
			s.it = ser.Iterator(s.it)
			for valType := s.it.Next(); valType != chunkenc.ValNone; valType = s.it.Next() {
				if valType != chunkenc.ValHistogram && valType != chunkenc.ValFloatHistogram {
					continue
				}
				_, s.fh = s.it.AtFloatHistogram(s.fh)
				if !histogram.IsExponentialSchema(s.fh.Schema) || !convertible(s.fh, s.sel.from) || s.fh.Validate() != nil {
					continue
				}
				fh := s.fh
				if fh.Schema > minSchema {
					fh = fh.CopyToSchema(minSchema)
				}
				boundaries = histogram.AppendClassicBoundaries(boundaries, fh)
			}
			if err := s.it.Err(); err != nil {
				return nil, err
			}
			// The samples of a series mostly have the same buckets, so drop the
			// duplicates after each series already.
			slices.Sort(boundaries)
			boundaries = slices.Compact(boundaries)
		}
	}
	return boundaries, nil
}

// classicSeriesBuilder collects the classic histogram series converted from
// native histograms, one native histogram sample after the other.
//
// A converted series is marked stale at the first sample of its native
// histogram that does not result in it anymore, e.g. because the native
// histogram went stale or its bucket layout changed, just like the scrape loop
// marks series stale that disappear from a target.
type classicSeriesBuilder struct {
	series []*series
	// byHash indexes series by label hash rather than by Labels.String().
	byHash map[uint64][]int
	// pool holds reusable fSample slices from previous groups whose
	// iterators are no longer in use.
	pool [][]fSample

	lsetBuilder         *labels.Builder
	nhcbCache, nheCache histogram.ClassicSeriesCache

	// st and t are the start timestamp and the timestamp of the current
	// sample.
	st, t int64
	// emitted and prevEmitted are the indices of the series emitted for the
	// current and for the previous sample of the current native histogram.
	emitted, prevEmitted []int
}

func newClassicSeriesBuilder() *classicSeriesBuilder {
	return &classicSeriesBuilder{
		byHash:      make(map[uint64][]int),
		lsetBuilder: labels.NewBuilder(labels.EmptyLabels()),
	}
}

// reset prepares b for the next histogramGroup, recycling any fSample slices
// that are not in use by an active iterator.
func (b *classicSeriesBuilder) reset() {
	for _, s := range b.series {
		if s.inUse == 0 && cap(s.floats) > 0 {
			b.pool = append(b.pool, s.floats[:0])
		}
	}
	clear(b.series)
	b.series = b.series[:0]
	clear(b.byHash)
	b.emitted = b.emitted[:0]
	b.prevEmitted = b.prevEmitted[:0]
}

// startSeries prepares for the samples of the next native histogram series.
func (b *classicSeriesBuilder) startSeries() {
	b.prevEmitted = b.prevEmitted[:0]
}

// startSample prepares for the series converted from the sample at t, with
// the start timestamp st.
func (b *classicSeriesBuilder) startSample(st, t int64) {
	b.st, b.t = st, t
	b.emitted = b.emitted[:0]
}

// emit appends the value v at the timestamp of the current sample, with its
// start timestamp, to the series with labels l. It is the emitSeriesFn of the
// conversion functions.
func (b *classicSeriesBuilder) emit(l labels.Labels, v float64) error {
	idx := -1
	if i := len(b.emitted); i < len(b.prevEmitted) && labels.Equal(b.series[b.prevEmitted[i]].lset, l) {
		idx = b.prevEmitted[i]
	} else {
		h := l.Hash()
		for _, candidate := range b.byHash[h] {
			if labels.Equal(b.series[candidate].lset, l) {
				idx = candidate
				break
			}
		}
		if idx == -1 {
			idx = len(b.series)
			b.byHash[h] = append(b.byHash[h], idx)
			var floats []fSample
			if n := len(b.pool); n > 0 {
				floats = b.pool[n-1]
				b.pool = b.pool[:n-1]
			}
			b.series = append(b.series, &series{lset: l, floats: floats})
		}
	}

	b.series[idx].floats = append(b.series[idx].floats, fSample{st: b.st, t: b.t, f: v})
	b.emitted = append(b.emitted, idx)
	return nil
}

// endSample marks the series emitted for the previous sample, but not for the
// current one, stale.
func (b *classicSeriesBuilder) endSample() {
	for _, idx := range b.prevEmitted {
		if floats := b.series[idx].floats; floats[len(floats)-1].t != b.t {
			b.series[idx].floats = append(floats, fSample{t: b.t, f: math.Float64frombits(value.StaleNaN)})
		}
	}
	b.emitted, b.prevEmitted = b.prevEmitted, b.emitted
}

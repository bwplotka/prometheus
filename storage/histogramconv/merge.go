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
	"slices"

	"github.com/prometheus/common/model"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
)

// storedWins lets stored data win over converted data for one histogramGroup,
// where the selector reads the histogram both stored and converted, and
// returns the converted series to return in addition to the stored ones:
//
//   - A converted sample is dropped where the stored data the selector reads
//     has a sample of the same histogram at the same timestamp. If the
//     converted sample before it is returned, and not a staleness marker, a
//     staleness marker is returned instead, so that the converted series ends
//     where the stored histogram takes over, even if the stored histogram has
//     other buckets.
//   - Converted series without samples other than staleness markers are
//     dropped.
//   - Converted series are merged into the stored series with the same
//     labels, see mergeSamples. In debug mode, their labels usually differ in
//     the StoredAsLabel.
//
// The samples of stored series returned unchanged are only read if needed.
func (s *seriesSet) storedWins(stored, converted []*series) ([]*series, error) {
	byLabels := make(map[uint64][]*series, len(converted))
	for _, c := range converted {
		if len(c.floats) > 0 {
			c.samples = make([]chunks.Sample, len(c.floats))
			for i, smpl := range c.floats {
				c.samples[i] = smpl
			}
			c.floats = c.floats[:0]
		}
		h := c.lset.Hash()
		byLabels[h] = append(byLabels[h], c)
	}

	// targets maps converted series to the stored series with the same
	// labels, which they are merged into. Both have unique labels.
	targets := map[*series]*series{}
	for _, st := range stored {
		for _, c := range byLabels[st.lset.Hash()] {
			if !labels.Equal(c.lset, st.lset) {
				continue
			}
			targets[c] = st
			if st.stored != nil {
				samples, err := s.readSamples(st.stored)
				if err != nil {
					return nil, err
				}
				st.samples, st.stored = samples, nil
			}
			break
		}
	}

	var (
		groupTS, ts []int64
		err         error
	)
	for _, st := range stored {
		if st.stored != nil {
			if ts, err = s.appendTimestamps(ts[:0], st.stored); err != nil {
				return nil, err
			}
		} else {
			ts = ts[:0]
			for _, smpl := range st.samples {
				if !isStale(smpl) {
					ts = append(ts, smpl.T())
				}
			}
		}
		groupTS = addTimestamps(groupTS, ts)
	}

	kept := converted[:0]
	for _, c := range converted {
		c.samples = shadow(c.samples, groupTS)
		if slices.ContainsFunc(c.samples, func(smpl chunks.Sample) bool { return !isStale(smpl) }) {
			kept = append(kept, c)
		}
	}
	unmerged := kept[:0]
	for _, c := range kept {
		st, ok := targets[c]
		if !ok {
			unmerged = append(unmerged, c)
			continue
		}
		st.samples = mergeSamples(st.samples, c.samples)
	}
	return unmerged, nil
}

// readSamples returns the samples of the series ser.
func (s *seriesSet) readSamples(ser storage.Series) ([]chunks.Sample, error) {
	var samples []chunks.Sample
	s.it = ser.Iterator(s.it)
	for vt := s.it.Next(); vt != chunkenc.ValNone; vt = s.it.Next() {
		samples = append(samples, atSample(s.it, vt))
	}
	return samples, s.it.Err()
}

// appendTimestamps appends the timestamps of the samples of the series ser to
// ts, without staleness markers.
func (s *seriesSet) appendTimestamps(ts []int64, ser storage.Series) ([]int64, error) {
	s.it = ser.Iterator(s.it)
	for vt := s.it.Next(); vt != chunkenc.ValNone; vt = s.it.Next() {
		var stale bool
		switch vt {
		case chunkenc.ValHistogram:
			_, s.h = s.it.AtHistogram(s.h)
			stale = value.IsStaleNaN(s.h.Sum)
		case chunkenc.ValFloatHistogram:
			_, s.fh = s.it.AtFloatHistogram(s.fh)
			stale = value.IsStaleNaN(s.fh.Sum)
		default:
			_, f := s.it.At()
			stale = value.IsStaleNaN(f)
		}
		if !stale {
			ts = append(ts, s.it.AtT())
		}
	}
	return ts, s.it.Err()
}

// histogramGroup holds the stored series and the series to convert from of one
// histogram.
type histogramGroup struct {
	// id holds the labels that identify the histogram.
	id      labels.Labels
	stored  []storage.Series
	sources []storage.Series
}

// histogramIndex groups series by the labels that identify their histogram:
// the labels of their series without the metric name, as all series of a
// selector have the same base name, without the StoredAsLabel, and without le
// where le does not identify the histogram.
type histogramIndex struct {
	groups []*histogramGroup
	byHash map[uint64][]*histogramGroup
	buf    []byte
	b      *labels.Builder
}

var (
	// Sorted label names to ignore when computing histogram identity.
	ignoredNative  = []string{model.MetricNameLabel, StoredAsLabel}
	ignoredClassic = []string{model.MetricNameLabel, StoredAsLabel, labels.BucketLabel}
)

func newHistogramIndex() *histogramIndex {
	return &histogramIndex{
		byHash: map[uint64][]*histogramGroup{},
		b:      labels.NewBuilder(labels.EmptyLabels()),
	}
}

// get returns the histogramGroup of the series with the given labels, creating
// it if needed. If ignoreLe is true, the le label is not part of the histogram
// identity.
func (idx *histogramIndex) get(lset labels.Labels, ignoreLe bool) *histogramGroup {
	ignored := ignoredNative
	if ignoreLe {
		ignored = ignoredClassic
	}
	var hash uint64
	hash, idx.buf = lset.HashWithoutLabels(idx.buf, ignored...)
	idx.b.Reset(lset)
	id := idx.b.Del(ignored...).Labels()
	for _, g := range idx.byHash[hash] {
		if labels.Equal(g.id, id) {
			return g
		}
	}
	g := &histogramGroup{id: id}
	idx.byHash[hash] = append(idx.byHash[hash], g)
	idx.groups = append(idx.groups, g)
	return g
}

// addTimestamps returns the sorted union of dst and ts.
func addTimestamps(dst, ts []int64) []int64 {
	switch {
	case len(ts) == 0 || slices.Equal(dst, ts):
		// The series of a classic histogram usually have the same timestamps.
		return dst
	case len(dst) == 0:
		return slices.Clone(ts)
	default:
		union := make([]int64, 0, len(dst)+len(ts))
		i, j := 0, 0
		for i < len(dst) && j < len(ts) {
			switch {
			case dst[i] < ts[j]:
				union = append(union, dst[i])
				i++
			case dst[i] > ts[j]:
				union = append(union, ts[j])
				j++
			default:
				union = append(union, dst[i])
				i++
				j++
			}
		}
		union = append(union, dst[i:]...)
		return append(union, ts[j:]...)
	}
}

// shadow drops the samples at the sorted timestamps ts. Where the sample
// before a dropped one is returned, and not a staleness marker, it returns a
// staleness marker instead of the dropped sample. It also drops the staleness
// markers that would be returned first or right after another staleness
// marker. It reuses samples.
func shadow(samples []chunks.Sample, ts []int64) []chunks.Sample {
	if len(ts) == 0 {
		return samples
	}
	out := samples[:0]
	j := 0
	for _, smpl := range samples {
		t := smpl.T()
		for j < len(ts) && ts[j] < t {
			j++
		}
		if j == len(ts) || ts[j] != t {
			if !isStale(smpl) || (len(out) > 0 && !isStale(out[len(out)-1])) {
				out = append(out, smpl)
			}
			continue
		}
		if len(out) > 0 && !isStale(out[len(out)-1]) {
			out = append(out, staleMarker(out[len(out)-1], t))
		}
	}
	return out
}

// mergeSamples merges the converted samples into the stored ones:
//
//   - Where both have a sample at the same timestamp, the one that is not a
//     staleness marker is returned, the stored one if both or neither are.
//   - Other staleness markers only end their own side, so they are dropped
//     where the sample before them is from the other side. Otherwise they
//     would hide the other side, e.g. where the stored series of a histogram
//     are marked stale after the NHCB they are converted from started, as it
//     happens after a configuration reload that enables
//     convert_classic_histograms_to_nhcb. Whether the other side continues
//     after them is not checked, as its next sample might be outside the
//     selected time range, e.g. in instant queries.
//   - Staleness markers that would be returned first or right after another
//     staleness marker are dropped, too.
//   - Where the returned samples switch sides, native histograms get an
//     unknown counter reset hint, unless they are gauge histograms, as their
//     hint refers to the sample before them on their own side. The chain
//     sample iterator of storage.ChainedSeriesMerge does the same.
func mergeSamples(stored, converted []chunks.Sample) []chunks.Sample {
	var (
		out = make([]chunks.Sample, 0, len(stored)+len(converted))
		// i and j are the indices of the next stored and converted samples.
		i, j int
		// lastConverted reports whether the last returned sample is a
		// converted one.
		lastConverted bool
	)
	for i < len(stored) || j < len(converted) {
		var (
			smpl        chunks.Sample
			isConverted bool
			// both reports whether both sides have a sample at the
			// timestamp of smpl.
			both bool
		)
		switch {
		case j == len(converted) || (i < len(stored) && stored[i].T() < converted[j].T()):
			smpl = stored[i]
			i++
		case i == len(stored) || converted[j].T() < stored[i].T():
			smpl, isConverted = converted[j], true
			j++
		default:
			smpl, both = stored[i], true
			if isStale(smpl) && !isStale(converted[j]) {
				smpl, isConverted = converted[j], true
			}
			i++
			j++
		}
		switchesSides := len(out) > 0 && lastConverted != isConverted
		if isStale(smpl) {
			if len(out) == 0 || isStale(out[len(out)-1]) || (!both && switchesSides) {
				continue
			}
		} else if switchesSides {
			smpl = withUnknownCounterReset(smpl)
		}
		out = append(out, smpl)
		lastConverted = isConverted
	}
	return out
}

// withUnknownCounterReset returns the sample s with an unknown counter reset
// hint if it is a native histogram, but not a gauge histogram, and s
// otherwise. It does not modify the histogram of s, which might be shared.
func withUnknownCounterReset(s chunks.Sample) chunks.Sample {
	switch smpl := s.(type) {
	case hSample:
		if hint := smpl.h.CounterResetHint; hint != histogram.UnknownCounterReset && hint != histogram.GaugeType {
			h := *smpl.h
			h.CounterResetHint = histogram.UnknownCounterReset
			smpl.h = &h
		}
		return smpl
	case fhSample:
		if hint := smpl.fh.CounterResetHint; hint != histogram.UnknownCounterReset && hint != histogram.GaugeType {
			fh := *smpl.fh
			fh.CounterResetHint = histogram.UnknownCounterReset
			smpl.fh = &fh
		}
		return smpl
	default:
		return s
	}
}

// isStale reports whether the sample s is a staleness marker.
func isStale(s chunks.Sample) bool {
	_, stale := representationOf(s)
	return stale
}

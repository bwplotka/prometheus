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
	"cmp"
	"slices"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
)

type samples []chunks.Sample

func (s samples) Get(i int) chunks.Sample { return s[i] }
func (s samples) Len() int                { return len(s) }

// fSample is a float sample.
type fSample struct {
	st, t int64
	f     float64
}

func (s fSample) T() int64                    { return s.t }
func (s fSample) ST() int64                   { return s.st }
func (s fSample) F() float64                  { return s.f }
func (fSample) H() *histogram.Histogram       { panic("H() called for fSample") }
func (fSample) FH() *histogram.FloatHistogram { panic("FH() called for fSample") }
func (fSample) Type() chunkenc.ValueType      { return chunkenc.ValFloat }
func (s fSample) Copy() chunks.Sample         { return s }

// floatSeriesIterator iterates over a slice of fSample without boxing each
// sample in a chunks.Sample interface value.
type floatSeriesIterator struct {
	samples []fSample
	idx     int
	owner   *series
}

func (it *floatSeriesIterator) reset(owner *series, samples []fSample) *floatSeriesIterator {
	it.samples = samples
	it.idx = -1
	it.owner = owner
	return it
}

func (it *floatSeriesIterator) release() {
	if it.owner != nil {
		it.owner.inUse--
		it.owner = nil
	}
}

func (it *floatSeriesIterator) Next() chunkenc.ValueType {
	it.idx++
	if it.idx >= len(it.samples) {
		it.release()
		return chunkenc.ValNone
	}
	return chunkenc.ValFloat
}

func (it *floatSeriesIterator) Seek(t int64) chunkenc.ValueType {
	if it.idx < 0 {
		it.idx = 0
	}
	if it.idx >= len(it.samples) {
		it.release()
		return chunkenc.ValNone
	}
	if it.samples[it.idx].t >= t {
		return chunkenc.ValFloat
	}
	i, _ := slices.BinarySearchFunc(it.samples[it.idx:], t, func(s fSample, target int64) int {
		return cmp.Compare(s.t, target)
	})
	it.idx += i
	if it.idx >= len(it.samples) {
		it.release()
		return chunkenc.ValNone
	}
	return chunkenc.ValFloat
}

func (it *floatSeriesIterator) At() (int64, float64) {
	s := it.samples[it.idx]
	return s.t, s.f
}

func (*floatSeriesIterator) AtHistogram(*histogram.Histogram) (int64, *histogram.Histogram) {
	panic("AtHistogram() called for floatSeriesIterator")
}

func (*floatSeriesIterator) AtFloatHistogram(*histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	panic("AtFloatHistogram() called for floatSeriesIterator")
}

func (it *floatSeriesIterator) AtT() int64  { return it.samples[it.idx].t }
func (it *floatSeriesIterator) AtST() int64 { return it.samples[it.idx].st }
func (*floatSeriesIterator) Err() error     { return nil }

// errIterator is a chunkenc.Iterator that immediately returns err.
type errIterator struct {
	err error
}

func (errIterator) Next() chunkenc.ValueType      { return chunkenc.ValNone }
func (errIterator) Seek(int64) chunkenc.ValueType { return chunkenc.ValNone }
func (errIterator) At() (int64, float64)          { return 0, 0 }
func (errIterator) AtHistogram(*histogram.Histogram) (int64, *histogram.Histogram) {
	return 0, nil
}

func (errIterator) AtFloatHistogram(*histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	return 0, nil
}
func (errIterator) AtT() int64    { return 0 }
func (errIterator) AtST() int64   { return 0 }
func (it errIterator) Err() error { return it.err }

// hSample is a native histogram sample with integer counts.
type hSample struct {
	st, t int64
	h     *histogram.Histogram
}

func (s hSample) T() int64                      { return s.t }
func (s hSample) ST() int64                     { return s.st }
func (hSample) F() float64                      { panic("F() called for hSample") }
func (s hSample) H() *histogram.Histogram       { return s.h }
func (s hSample) FH() *histogram.FloatHistogram { return s.h.ToFloat(nil) }
func (hSample) Type() chunkenc.ValueType        { return chunkenc.ValHistogram }
func (s hSample) Copy() chunks.Sample           { return hSample{st: s.st, t: s.t, h: s.h.Copy()} }

// fhSample is a native histogram sample with float counts.
type fhSample struct {
	st, t int64
	fh    *histogram.FloatHistogram
}

func (s fhSample) T() int64                      { return s.t }
func (s fhSample) ST() int64                     { return s.st }
func (fhSample) F() float64                      { panic("F() called for fhSample") }
func (fhSample) H() *histogram.Histogram         { panic("H() called for fhSample") }
func (s fhSample) FH() *histogram.FloatHistogram { return s.fh }
func (fhSample) Type() chunkenc.ValueType        { return chunkenc.ValFloatHistogram }
func (s fhSample) Copy() chunks.Sample           { return fhSample{st: s.st, t: s.t, fh: s.fh.Copy()} }

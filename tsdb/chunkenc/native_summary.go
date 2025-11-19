// Copyright 2025 The Prometheus Authors
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

package chunkenc

import (
	"encoding/binary"
	"math"

	"github.com/prometheus/prometheus/model/histogram"
)

type SummaryChunk struct {
	b bstream
}

var (
	// Position within the header bytes at the start of the stream.
	summaryFlagPos    = 2
	summaryHeaderSize = 3
)

func NewSummaryChunk() *SummaryChunk {
	b := make([]byte, summaryHeaderSize, chunkAllocationSize)
	return &SummaryChunk{b: bstream{stream: b, count: 0}}
}

func (c *SummaryChunk) Reset(stream []byte) {
	c.b.Reset(stream)
}

// Encoding returns the encoding type.
func (*SummaryChunk) Encoding() Encoding {
	return EncSummary
}

// Bytes returns the underlying byte slice of the chunk.
func (c *SummaryChunk) Bytes() []byte {
	return c.b.bytes()
}

// NumSamples returns the number of samples in the chunk.
func (c *SummaryChunk) NumSamples() int {
	return int(binary.BigEndian.Uint16(c.Bytes()))
}

// Compact implements the Chunk interface by removing unused space
func (c *SummaryChunk) Compact() {
	if l := len(c.b.stream); cap(c.b.stream) > l+chunkCompactCapacityThreshold {
		buf := make([]byte, l)
		copy(buf, c.b.stream)
		c.b.stream = buf
	}
}

// Appender implements the Chunk interface.
func (c *SummaryChunk) Appender() (Appender, error) {
	if len(c.b.stream) == histogramHeaderSize { // Avoid allocating an Iterator when chunk is empty.
		return &SummaryAppender{b: &c.b, t: math.MinInt64, sum: xorValue{leading: 0xff}, cnt: xorValue{leading: 0xff}}, nil
	}
	it := c.iterator(nil)

	// To get an appender, we must know the state it would have if we had
	// appended all existing data from scratch. We iterate through the end
	// and populate via the iterator's state.
	for it.Next() == ValSummary {
	}
	if err := it.Err(); err != nil {
		return nil, err
	}

	qValues := make([]xorValue, len(it.quantileValues))
	for i := 0; i < len(it.quantileValues); i++ {
		qValues[i] = xorValue{
			value:    it.quantileValues[i],
			leading:  it.quantileValuesLeading[i],
			trailing: it.quantileValuesTrailing[i],
		}
	}

	a := &SummaryAppender{
		b: &c.b,

		schema:          it.schema,
		t:               it.t,
		tDelta:          it.tDelta,
		cnt:             it.cnt,
		sum:             it.sum,
		quantileTargets: it.quantileTargets,
		quantilValues:   qValues,
	}
	return a, nil
}

// Iterator implements the Chunk interface.
func (c *SummaryChunk) Iterator(it Iterator) Iterator {
	return c.iterator(it)
}

func (c *SummaryChunk) iterator(it Iterator) *summaryIterator {
	// This comment is copied from FloatHistogramChunk.iterator:
	//   Should iterators guarantee to act on a copy of the data so it doesn't lock append?
	//   When using striped locks to guard access to chunks, probably yes.
	//   Could only copy data if the chunk is not completed yet.
	if summaryIter, ok := it.(*summaryIterator); ok {
		summaryIter.Reset(c.b.bytes())
		return summaryIter
	}
	return newSummaryIterator(c.b.bytes())
}

func newSummaryIterator(b []byte) *summaryIterator {
	it := &summaryIterator{
		br:       newBReader(b[summaryHeaderSize:]),
		numTotal: binary.BigEndian.Uint16(b),
		t:        math.MinInt64,
	}
	it.counterResetHeader = CounterResetHeader(b[summaryFlagPos] & CounterResetHeaderMask)
	return it
}

type SummaryAppender struct {
	b *bstream

	// Layout:
	schema int32
	// customValues is read only after the first sample is appended.
	quantileTargets []float64

	// Although we intend to start new chunks on counter resets, we still
	// have to handle negative deltas for gauge histograms. Therefore, even
	// deltas are signed types here (even for tDelta to not treat that one
	// specially).
	t, tDelta int64
	sum, cnt  xorValue

	quantilValues []xorValue
}

func (*SummaryAppender) Append(int64, float64) {
	panic("appended a float sample to a summary chunk")
}

func (*SummaryAppender) AppendHistogram(*HistogramAppender, int64, *histogram.Histogram, bool) (Chunk, bool, Appender, error) {
	panic("appended a histogram sample to a summary chunk")
}

func (*SummaryAppender) AppendFloatHistogram(*FloatHistogramAppender, int64, *histogram.FloatHistogram, bool) (Chunk, bool, Appender, error) {
	panic("appended a float histogram sample to a summary chunk")
}

func (a *FloatHistogramAppender) GetCounterResetSummaryHeader() CounterResetHeader {
	return CounterResetHeader(a.b.bytes()[summaryFlagPos] & CounterResetHeaderMask)
}

func (a *FloatHistogramAppender) setCounterResetSummaryHeader(cr CounterResetHeader) {
	a.b.bytes()[summaryFlagPos] = (a.b.bytes()[summaryFlagPos] & (^CounterResetHeaderMask)) | (byte(cr) & CounterResetHeaderMask)
}
func (a *SummaryAppender) NumSamples() int {
	return int(binary.BigEndian.Uint16(a.b.bytes()))
}

func (a *SummaryAppender) AppendSummary(prev *SummaryAppender, t int64, h *histogram.FloatHistogram, appendOnly bool) (Chunk, bool, Appender, error) {
	// TODO: Implement actual summary encoding
	return nil, false, a, nil
}

type summaryIterator struct {
	br bstreamReader

	numTotal uint16
	numRead  uint16

	counterResetHeader CounterResetHeader

	// Layout:
	schema          int32
	quantileTargets []float64

	t, tDelta int64

	// All Gorilla xor encoded.
	sum, cnt xorValue

	quantileValues         []float64
	quantileValuesLeading  []uint8
	quantileValuesTrailing []uint8

	err error
}

func (it *summaryIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	if fh == nil {
		fh = &histogram.FloatHistogram{}
	}

	fh.CounterResetHint = histogram.CounterResetHint(it.counterResetHeader)
	fh.Count = it.cnt.value
	fh.Sum = it.sum.value

	// Set quantiles
	if fh.QuantileTargets == nil || len(fh.QuantileTargets) != len(it.quantileTargets) {
		fh.QuantileTargets = make([]float64, len(it.quantileTargets))
	}
	copy(fh.QuantileTargets, it.quantileTargets)

	if fh.QuantileValues == nil || len(fh.QuantileValues) != len(it.quantileValues) {
		fh.QuantileValues = make([]float64, len(it.quantileValues))
	}
	copy(fh.QuantileValues, it.quantileValues)

	return it.t, fh
}

func (it *summaryIterator) AtHistogram(*histogram.Histogram) (int64, *histogram.Histogram) {
	panic("summary iterator does not support integer histograms")
}

func (it *summaryIterator) AtT() int64 {
	return it.t
}

func (it *summaryIterator) At() (int64, float64) {
	return it.t, 0
}

func (it *summaryIterator) Err() error {
	return it.err
}

func (it *summaryIterator) Reset(b []byte) {
	// The first 3 bytes contain chunk headers.
	// We skip that for actual samples.
	it.br = newBReader(b[summaryHeaderSize:])
	it.numTotal = binary.BigEndian.Uint16(b)
	it.numRead = 0

	it.counterResetHeader = CounterResetHeader(b[2] & CounterResetHeaderMask)

	it.t, it.tDelta = 0, 0
	it.cnt, it.sum = xorValue{}, xorValue{}
	it.quantileValues = nil
	it.quantileTargets = nil
	it.quantileValuesLeading = nil
	it.quantileValuesTrailing = nil
	it.err = nil
}

func (it *summaryIterator) Next() ValueType {
	// need to do
	return ValNone
}

func (it *summaryIterator) Seek(t int64) ValueType {
	// need to do
	return ValNone
}

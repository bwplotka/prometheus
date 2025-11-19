package chunkenc

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/tsdb/tsdbutil"
)

type sampleCase struct {
	name string
	h    []*histogram.FloatHistogram
}

type fmtCase struct {
	name       string
	newChunkFn func() Chunk
}

// TODO
// do another test like this comparing
// SEND FLOAT HISTOGRAMS (not summaries)
// 	fmtCases := []fmtCase{
//		{
//			name:       "FloatHistogram",
//			newChunkFn: func() Chunk { return NewFloatHistogramChunk() },
//		},
//		{
//			name:       "FloatHistogramUsingNativeSummary",
//			newChunkFn: func() Chunk { return NewFloatHistogramChunk2{} },
//		},

func foreachFmtSampleCase(b *testing.B, fn func(b *testing.B, f fmtCase, s sampleCase)) {
	const nSamples = 120

	sampleCases := []sampleCase{
		{
			name: "5q/values=linear",
			h:    tsdbutil.GenerateTestNativeSummaries(nSamples),
		},
		{
			name: "5q/values=constant",
			h:    generateConstantValueSummaries(nSamples, 5),
		},
		{
			name: "5q/values=random",
			h:    generateRandomValueSummaries(nSamples, 5),
		},
		{
			name: "10q/values=linear",
			h:    generateLinearValueSummaries(nSamples, 10),
		},
		{
			name: "10q/values=random",
			h:    generateRandomValueSummaries(nSamples, 10),
		},
	}

	fmtCases := []fmtCase{
		{
			name:       "FloatHistogram",
			newChunkFn: func() Chunk { return NewFloatHistogramChunk() },
		},
		{
			name:       "ClassicSummarySimulated",
			newChunkFn: func() Chunk { return simulatingClassicSummary{} },
		},
	}

	for _, f := range fmtCases {
		for _, s := range sampleCases {
			b.Run(f.name+"/"+s.name, func(b *testing.B) {
				fn(b, f, s)
			})
		}
	}
}

func generateConstantValueSummaries(n, numQuantiles int) []*histogram.FloatHistogram {
	targets := make([]float64, numQuantiles)
	values := make([]float64, numQuantiles)
	for j := 0; j < numQuantiles; j++ {
		targets[j] = float64(j+1) / float64(numQuantiles+1)
		values[j] = float64(j * 10)
	}

	r := make([]*histogram.FloatHistogram, n)
	for i := 0; i < n; i++ {
		r[i] = &histogram.FloatHistogram{
			Count:           5 + float64(i*4),
			Sum:             10 * float64(i+1),
			Schema:          histogram.NativeSummarySchema,
			QuantileTargets: targets,
			QuantileValues:  values,
		}
		if i > 0 {
			r[i].CounterResetHint = histogram.NotCounterReset
		}
	}
	return r
}

func generateLinearValueSummaries(n, numQuantiles int) []*histogram.FloatHistogram {
	targets := make([]float64, numQuantiles)
	for j := 0; j < numQuantiles; j++ {
		targets[j] = float64(j+1) / float64(numQuantiles+1)
	}

	r := make([]*histogram.FloatHistogram, n)
	for i := 0; i < n; i++ {
		values := make([]float64, numQuantiles)
		for j := 0; j < numQuantiles; j++ {
			values[j] = float64(i+1) + float64(j)
		}

		r[i] = &histogram.FloatHistogram{
			Count:           5 + float64(i*4),
			Sum:             10 * float64(i+1),
			Schema:          histogram.NativeSummarySchema,
			QuantileTargets: targets,
			QuantileValues:  values,
		}
		if i > 0 {
			r[i].CounterResetHint = histogram.NotCounterReset
		}
	}
	return r
}

func generateRandomValueSummaries(n, numQuantiles int) []*histogram.FloatHistogram {
	r := rand.New(rand.NewSource(10))

	targets := make([]float64, numQuantiles)
	for j := 0; j < numQuantiles; j++ {
		targets[j] = float64(j+1) / float64(numQuantiles+1)
	}

	rs := make([]*histogram.FloatHistogram, n)
	// this is for base val
	val := 100.0
	for i := 0; i < n; i++ {
		values := make([]float64, numQuantiles)
		for j := 0; j < numQuantiles; j++ {
			// we wll just add noise here
			values[j] = val + float64(j*10) + r.Float64()*100 - 50
		}

		rs[i] = &histogram.FloatHistogram{
			Count:           5 + float64(i*4),
			Sum:             10 * float64(i+1),
			Schema:          histogram.NativeSummarySchema,
			QuantileTargets: targets,
			QuantileValues:  values,
		}
		if i > 0 {
			rs[i].CounterResetHint = histogram.NotCounterReset
		}
	}
	return rs
}

func BenchmarkAppender(b *testing.B) {
	foreachFmtSampleCase(b, func(b *testing.B, f fmtCase, s sampleCase) {
		b.ReportAllocs()

		for b.Loop() {
			c := f.newChunkFn()

			a, err := c.Appender()
			if err != nil {
				b.Fatalf("get appender: %s", err)
			}
			for j, h := range s.h {
				newChunk, _, newApp, err := a.AppendFloatHistogram(nil, int64(j*10000), h, false)
				if err != nil {
					b.Fatalf("append sample %d: %s", j, err)
				}
				if newChunk != nil {
					b.Logf("New chunk created at sample %d, old chunk has %d samples", j, c.NumSamples()) // Logging in bench?
					c = newChunk
				}
				a = newApp
			}
			b.ReportMetric(float64(len(c.Bytes())), "B/chunk")

			require.Equal(b, len(s.h), c.NumSamples())
		}
	})
}

type simulatingClassicSummary struct {
	countChunk, sumChunk Chunk
	quantileChunks       []Chunk
}

func (s *simulatingClassicSummary) Appender() (Appender, error) {
	// Fetch those appenders from each.
	return s.Appender()
}

type simulatingClassicSummaryApp struct {
	countChunk, sumChunk Appender
	quantileChunks       []Appender
}

func (a *simulatingClassicSummaryApp) AppendFloatHistogram(prev *FloatHistogramAppender, t int64, h *histogram.FloatHistogram, appendOnly bool) (c Chunk, isRecoded bool, app Appender, err error) {
	// Append to all 5-10 chunks

	a.countChunk.Append(t, h.Count)
	a.sumChunk.Append(t, h.Sum)

	for i, val := range h.QuantileValues {
		a.quantileChunks[i].Append(t, val)
	}
	return nil //. . .. . ./
}

func BenchmarkIterator(b *testing.B) {
	foreachFmtSampleCase(b, func(b *testing.B, f fmtCase, s sampleCase) {
		c := f.newChunkFn()
		app, err := c.Appender()
		if err != nil {
			b.Fatalf("get appender: %s", err)
		}

		for j, h := range s.h {
			_, _, newApp, err := app.AppendFloatHistogram(nil, int64(j*10000), h, false)
			if err != nil {
				b.Fatalf("append sample %d: %s", j, err)
			}
			app = newApp
		}

		require.Equal(b, len(s.h), c.NumSamples())
		b.ReportMetric(float64(len(c.Bytes())), "B/chunk")
		b.ReportAllocs()

		var it Iterator
		for b.Loop() {
			it = c.Iterator(it)
			for it.Next() != ValNone {
				_, _ = it.AtFloatHistogram(nil)
			}
			if err := it.Err(); err != nil {
				b.Fatalf("iterator error: %s", err)
			}
		}
	})
}

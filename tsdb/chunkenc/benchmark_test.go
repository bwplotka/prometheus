package chunkenc

import (
	"testing"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/tsdb/tsdbutil"
	"github.com/stretchr/testify/require"
)

type sampleCase struct {
	name string
	h    []*histogram.FloatHistogram
}

type fmtCase struct {
	name       string
	newChunkFn func() Chunk
}

func foreachFmtSampleCase(b *testing.B, fn func(b *testing.B, f fmtCase, s sampleCase)) {
	const nSamples = 120

	sampleCases := []sampleCase{
		{
			name: "NativeSummary",
			h:    tsdbutil.GenerateTestNativeSummaries(nSamples),
		},
	}

	fmtCases := []fmtCase{
		{
			name:       "FloatHistogram",
			newChunkFn: func() Chunk { return NewFloatHistogramChunk() },
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
					b.Logf("New chunk created at sample %d, old chunk has %d samples", j, c.NumSamples())
					c = newChunk
				}
				a = newApp
			}
			b.ReportMetric(float64(len(c.Bytes())), "B/chunk")

			require.Equal(b, len(s.h), c.NumSamples())
		}
	})
}

func BenchmarkIterator(b *testing.B) {
}

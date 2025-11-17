package chunkenc

import (
	"math/rand"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/timestamp"
	"github.com/stretchr/testify/require"
)

type sampleCase struct {
	name string
	h    histogram.Histogram
}

type fmtCase struct {
	name       string
	newChunkFn func() Chunk
}

func foreachFmtSampleCase(b *testing.B, fn func(b *testing.B, f fmtCase, s sampleCase)) {
	const nSamples = 120

	d, err := time.Parse(time.DateTime, "2025-11-04 10:01:05")
	require.NoError(b, err)

	var (
		r      = rand.New(rand.NewSource(1))
		initST = timestamp.FromTime(d) // Use realistic timestamp.
		initV  = 1243535.123
	)

	sampleCases := []sampleCase{}
}

func BenchmarkAppender(b *testing.B) {
	foreachFmtSampleCase(b, func(b *testing.B, f fmtCase, s sampleCase) {
		b.ReportAllocs()

		for b.Loop() {
			c := f.newChunkFn()
		}
	})
}

func BenchmarkIterator(b *testing.B) {
}

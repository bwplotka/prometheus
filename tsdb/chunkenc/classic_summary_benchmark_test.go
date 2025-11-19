package chunkenc

import (
	"math/rand"
	"testing"
)

func BenchmarkAppenderClassicSummary(b *testing.B) {
	const nSamples = 120

	testCases := []struct {
		name         string
		numQuantiles int
	}{
		{name: "5q/values=linear", numQuantiles: 5},
		{name: "5q/values=constant", numQuantiles: 5},
		{name: "5q/values=random", numQuantiles: 5},
		{name: "10q/values=linear", numQuantiles: 10},
		{name: "10q/values=random", numQuantiles: 10},
	}

	for _, tc := range testCases {
		b.Run("XOR-N+2/"+tc.name, func(b *testing.B) {
			b.ReportAllocs()

			// for each
			// if linear then
			// for generateLinearValueSummaries
			//   q1, q2
			// hERE IS WHERE YOU generate shit, here is not measured
			for b.Loop() {
				// HEere is measured stuff
				r := rand.New(rand.NewSource(10))

				countChunk := NewXORChunk()
				sumChunk := NewXORChunk()
				quantileChunks := make([]Chunk, tc.numQuantiles)
				for i := 0; i < tc.numQuantiles; i++ {
					quantileChunks[i] = NewXORChunk()
				}

				countApp, _ := countChunk.Appender()
				sumApp, _ := sumChunk.Appender()
				quantileApps := make([]Appender, tc.numQuantiles)
				for i := 0; i < tc.numQuantiles; i++ {
					quantileApps[i], _ = quantileChunks[i].Appender()
				}

				// base val for rand
				val := 100.0
				for j := range nSamples {
					ts := int64(j * 10000)

					count := 5.0 + float64(j*4)
					countApp.Append(ts, count)

					sum := 10.0 * float64(j+1)
					sumApp.Append(ts, sum)

					for q := 0; q < tc.numQuantiles; q++ {
						var value float64
						switch tc.name {
						case "5q/values=constant", "10q/values=constant":
							value = float64(q * 10)
						case "5q/values=random", "10q/values=random":
							value = val + float64(q*10) + r.Float64()*100 - 50
						default:
							value = float64(j+1) + float64(q)
						}
						quantileApps[q].Append(ts, value)
					}
				}

				totalSize := len(countChunk.Bytes()) + len(sumChunk.Bytes())
				for i := 0; i < tc.numQuantiles; i++ {
					totalSize += len(quantileChunks[i].Bytes())
				}

				b.ReportMetric(float64(totalSize), "B/total")
				b.ReportMetric(float64(tc.numQuantiles+2), "chunks")
			}
		})
	}
}

func BenchmarkIteratorClassicSummary(b *testing.B) {
	const nSamples = 120

	testCases := []struct {
		name         string
		numQuantiles int
	}{
		{name: "5q/values=linear", numQuantiles: 5},
		{name: "5q/values=constant", numQuantiles: 5},
		{name: "5q/values=random", numQuantiles: 5},
		{name: "10q/values=linear", numQuantiles: 10},
		{name: "10q/values=random", numQuantiles: 10},
	}

	for _, tc := range testCases {
		b.Run("XOR-N+2/"+tc.name, func(b *testing.B) {
			r := rand.New(rand.NewSource(10))

			countChunk := NewXORChunk()
			sumChunk := NewXORChunk()
			quantileChunks := make([]Chunk, tc.numQuantiles)
			for i := 0; i < tc.numQuantiles; i++ {
				quantileChunks[i] = NewXORChunk()
			}

			countApp, _ := countChunk.Appender()
			sumApp, _ := sumChunk.Appender()
			quantileApps := make([]Appender, tc.numQuantiles)
			for i := 0; i < tc.numQuantiles; i++ {
				quantileApps[i], _ = quantileChunks[i].Appender()
			}

			val := 100.0
			for j := range nSamples {
				ts := int64(j * 10000)

				count := 5.0 + float64(j*4)
				countApp.Append(ts, count)

				sum := 10.0 * float64(j+1)
				sumApp.Append(ts, sum)

				for q := 0; q < tc.numQuantiles; q++ {
					var value float64
					switch tc.name {
					case "5q/values=constant", "10q/values=constant":
						value = float64(q * 10)
					case "5q/values=random", "10q/values=random":
						value = val + float64(q*10) + r.Float64()*100 - 50
					default:
						value = float64(j+1) + float64(q)
					}
					quantileApps[q].Append(ts, value)
				}
			}

			totalSize := len(countChunk.Bytes()) + len(sumChunk.Bytes())
			for i := 0; i < tc.numQuantiles; i++ {
				totalSize += len(quantileChunks[i].Bytes())
			}

			b.ReportMetric(float64(totalSize), "B/total")
			b.ReportMetric(float64(tc.numQuantiles+2), "chunks")
			b.ReportAllocs()

			for b.Loop() {
				itCount := countChunk.Iterator(nil)
				for itCount.Next() != ValNone {
					_, _ = itCount.At()
				}

				itSum := sumChunk.Iterator(nil)
				for itSum.Next() != ValNone {
					_, _ = itSum.At()
				}

				for i := 0; i < tc.numQuantiles; i++ {
					it := quantileChunks[i].Iterator(nil)
					for it.Next() != ValNone {
						_, _ = it.At()
					}
				}
			}
		})
	}
}

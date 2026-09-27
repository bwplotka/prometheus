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

package scrape

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/util/teststorage"
)

// makeBenchClassicHistograms builds a realistic exposition with 1 histogram
// metric family containing numSeries label sets, each with numBuckets finite
// buckets (+Inf, _sum, _count), whose cumulative counts grow with scrapeStep.
func makeBenchClassicHistograms(numSeries, numBuckets, scrapeStep int, isOM bool) []byte {
	var sb bytes.Buffer
	sb.WriteString("# HELP bench_http_request_duration_seconds Request latency distribution.\n")
	sb.WriteString("# TYPE bench_http_request_duration_seconds histogram\n")
	stepMult := scrapeStep + 1
	for i := range numSeries {
		lbls := fmt.Sprintf(`job="api",instance="inst-%d",handler="/api/%d",method="%s",status="%s",series="%d"`,
			i%20, i%50, []string{"GET", "POST"}[i%2], []string{"200", "400", "500"}[i%3], i)
		cum := 0
		for b := range numBuckets {
			cum += (b%3 + 1) * stepMult
			_, _ = fmt.Fprintf(&sb, "bench_http_request_duration_seconds_bucket{%s,le=\"%d.0\"} %d\n", lbls, b+1, cum)
		}
		cum += stepMult
		_, _ = fmt.Fprintf(&sb, "bench_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", lbls, cum)
		_, _ = fmt.Fprintf(&sb, "bench_http_request_duration_seconds_sum{%s} %.2f\n", lbls, float64(cum)*1.25)
		_, _ = fmt.Fprintf(&sb, "bench_http_request_duration_seconds_count{%s} %d\n", lbls, cum)
		if isOM {
			_, _ = fmt.Fprintf(&sb, "bench_http_request_duration_seconds_created{%s} 1700000000.0\n", lbls)
		}
	}
	if isOM {
		sb.WriteString("# EOF\n")
	}
	return sb.Bytes()
}

func BenchmarkScrapeClassicVsNHCB(b *testing.B) {
	const numSeries = 100
	for _, numBuckets := range []int{10, 30} {
		promText := makeBenchClassicHistograms(numSeries, numBuckets, 1, false)
		omText := makeBenchClassicHistograms(numSeries, numBuckets, 1, true)
		promProto := promTextToProto(b, promText)

		for _, format := range []struct {
			name        string
			contentType string
			payload     []byte
		}{
			{name: "PromText", contentType: "text/plain", payload: promText},
			{name: "OMText", contentType: "application/openmetrics-text", payload: omText},
			{name: "PromProto", contentType: "application/vnd.google.protobuf", payload: promProto},
		} {
			for _, mode := range []struct {
				name          string
				convertToNHCB bool
				alwaysClassic bool
			}{
				{name: "classic", convertToNHCB: false, alwaysClassic: false},
				{name: "nhcb", convertToNHCB: true, alwaysClassic: false},
				{name: "both", convertToNHCB: true, alwaysClassic: true},
			} {
				b.Run(fmt.Sprintf("buckets=%d/fmt=%s/mode=%s/target=noStorage", numBuckets, format.name, mode.name), func(b *testing.B) {
					a := teststorage.NewAppendable().SkipRecording(true)
					sl, _ := newTestScrapeLoop(b, withAppendable(a, true), func(sl *scrapeLoop) {
						sl.enableNativeHistogramScraping = true
						sl.convertClassicHistToNHCB = mode.convertToNHCB
						sl.alwaysScrapeClassicHist = mode.alwaysClassic
					})
					ts := time.Unix(1000, 0)
					// Warm up scrape cache.
					app := sl.appender()
					_, _, _, err := app.append(format.payload, format.contentType, ts)
					require.NoError(b, err)
					require.NoError(b, app.Commit())

					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						ts = ts.Add(15 * time.Second)
						app := sl.appender()
						_, _, _, err := app.append(format.payload, format.contentType, ts)
						if err != nil {
							b.Fatal(err)
						}
						if err := app.Rollback(); err != nil {
							b.Fatal(err)
						}
					}
				})

				b.Run(fmt.Sprintf("buckets=%d/fmt=%s/mode=%s/target=tsdbWarmCommit", numBuckets, format.name, mode.name), func(b *testing.B) {
					s := teststorage.New(b)
					sl, _ := newTestScrapeLoop(b, withAppendable(s, true), func(sl *scrapeLoop) {
						sl.enableNativeHistogramScraping = true
						sl.convertClassicHistToNHCB = mode.convertToNHCB
						sl.alwaysScrapeClassicHist = mode.alwaysClassic
					})
					ts := time.Unix(1000, 0)
					// Warm up scrape cache & create series in TSDB Head.
					app := sl.appender()
					_, _, _, err := app.append(format.payload, format.contentType, ts)
					require.NoError(b, err)
					require.NoError(b, app.Commit())

					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						ts = ts.Add(15 * time.Second)
						app := sl.appender()
						_, _, _, err := app.append(format.payload, format.contentType, ts)
						if err != nil {
							b.Fatal(err)
						}
						if err := app.Commit(); err != nil {
							b.Fatal(err)
						}
					}
				})

				b.Run(fmt.Sprintf("buckets=%d/fmt=%s/mode=%s/target=tsdbColdRollback", numBuckets, format.name, mode.name), func(b *testing.B) {
					s := teststorage.New(b)
					sl, _ := newTestScrapeLoop(b, withAppendable(s, true), func(sl *scrapeLoop) {
						sl.enableNativeHistogramScraping = true
						sl.convertClassicHistToNHCB = mode.convertToNHCB
						sl.alwaysScrapeClassicHist = mode.alwaysClassic
					})
					ts := time.Unix(1000, 0)

					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						sl.cache = newScrapeCache(sl.metrics)
						ts = ts.Add(15 * time.Second)
						app := sl.appender()
						_, _, _, err := app.append(format.payload, format.contentType, ts)
						if err != nil {
							b.Fatal(err)
						}
						if err := app.Rollback(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func dirSizeBytes(tb testing.TB, path string) int64 {
	tb.Helper()
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	require.NoError(tb, err)
	return total
}

// BenchmarkTSDBFootprintClassicVsNHCB ingests 200 histograms over 120 scrapes
// (2 hours at 1m interval) into a fresh TSDB, measures Head series count, WAL
// size, and compacted Block (index + chunks) size.
func BenchmarkTSDBFootprintClassicVsNHCB(b *testing.B) {
	const (
		numSeries  = 200
		numScrapes = 120
	)
	for _, numBuckets := range []int{10, 30} {
		payloads := make([][]byte, numScrapes)
		for step := range numScrapes {
			payloads[step] = makeBenchClassicHistograms(numSeries, numBuckets, step, false)
		}

		for _, mode := range []struct {
			name          string
			convertToNHCB bool
			alwaysClassic bool
		}{
			{name: "classic", convertToNHCB: false, alwaysClassic: false},
			{name: "nhcb", convertToNHCB: true, alwaysClassic: false},
			{name: "both", convertToNHCB: true, alwaysClassic: true},
		} {
			b.Run(fmt.Sprintf("buckets=%d/mode=%s", numBuckets, mode.name), func(b *testing.B) {
				var (
					activeSeries  uint64
					headHeapBytes uint64
					walBytes      int64
					blockBytes    int64
					indexBytes    int64
					chunksBytes   int64
				)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					b.StopTimer()
					s := teststorage.New(b)
					runtime.GC()
					runtime.GC()
					var msBefore, msAfter runtime.MemStats
					runtime.ReadMemStats(&msBefore)

					sl, _ := newTestScrapeLoop(b, withAppendable(s, true), func(sl *scrapeLoop) {
						sl.enableNativeHistogramScraping = true
						sl.convertClassicHistToNHCB = mode.convertToNHCB
						sl.alwaysScrapeClassicHist = mode.alwaysClassic
					})
					baseTime := time.Unix(0, 0).UTC()
					b.StartTimer()

					for step := range numScrapes {
						ts := baseTime.Add(time.Duration(step) * time.Minute)
						app := sl.appender()
						_, _, _, err := app.append(payloads[step], "text/plain", ts)
						if err != nil {
							b.Fatal(err)
						}
						if err := app.Commit(); err != nil {
							b.Fatal(err)
						}
					}

					b.StopTimer()
					sl.cache = nil
					sl = nil
					runtime.GC()
					runtime.GC()
					runtime.ReadMemStats(&msAfter)
					if msAfter.HeapAlloc > msBefore.HeapAlloc {
						headHeapBytes = msAfter.HeapAlloc - msBefore.HeapAlloc
					}

					activeSeries = s.Head().NumSeries()
					walBytes = dirSizeBytes(b, filepath.Join(s.Dir(), "wal"))

					rh := tsdb.NewRangeHead(s.Head(), 0, int64(numScrapes)*60000)
					require.NoError(b, s.CompactHead(rh))
					blocks := s.Blocks()
					require.Len(b, blocks, 1)
					blockDir := filepath.Join(s.Dir(), blocks[0].Meta().ULID.String())
					blockBytes = dirSizeBytes(b, blockDir)
					idxStat, err := os.Stat(filepath.Join(blockDir, "index"))
					require.NoError(b, err)
					indexBytes = idxStat.Size()
					chunksBytes = dirSizeBytes(b, filepath.Join(blockDir, "chunks"))
					runtime.KeepAlive(s)
					_ = s.Close()
					b.StartTimer()
				}
				b.ReportMetric(float64(activeSeries), "series")
				b.ReportMetric(float64(headHeapBytes)/1024.0, "head_heap_KiB")
				b.ReportMetric(float64(walBytes)/1024.0, "wal_KiB")
				b.ReportMetric(float64(blockBytes)/1024.0, "block_KiB")
				b.ReportMetric(float64(indexBytes)/1024.0, "index_KiB")
				b.ReportMetric(float64(chunksBytes)/1024.0, "chunks_KiB")
			})
		}
	}
}

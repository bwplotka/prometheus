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

package histogramconv_test

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/storage/histogramconv"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/teststorage"
)

// populateBenchTSDB populates st with numSeries series over numSamples
// intervals (1m step) for:
//   - bench_classic_{bucket,count,sum}: classic histogram only
//   - bench_nhcb: NHCB native histogram only
//   - bench_nhe: Exponential native histogram (schema 2, ~numBuckets buckets) only
//   - bench_migrated[_{bucket,count,sum}]: Classic for first half, NHCB for second half (with staleness markers at midpoint)
//   - bench_overlap[_{bucket,count,sum}]: Both Classic and NHCB stored at every timestamp
//   - bench_requests_total: regular float counter series
func populateBenchTSDB(tb testing.TB, st *teststorage.TestStorage, numSeries, numBuckets, numSamples int) {
	tb.Helper()
	ctx := context.Background()

	customValues := make([]float64, numBuckets)
	for b := range numBuckets {
		customValues[b] = float64(b + 1)
	}
	staleFloat := math.Float64frombits(value.StaleNaN)
	midSample := numSamples / 2

	baseLabels := func(name string, i int, extra ...string) labels.Labels {
		kv := []string{
			model.MetricNameLabel, name,
			"job", "api",
			"instance", fmt.Sprintf("inst-%d", i%20),
			"handler", fmt.Sprintf("/api/%d", i%50),
			"method", []string{"GET", "POST"}[i%2],
			"status", []string{"200", "400", "500"}[i%3],
			"series", strconv.Itoa(i),
		}
		kv = append(kv, extra...)
		return labels.FromStrings(kv...)
	}

	appendClassic := func(app storage.Appender, metricPrefix string, i int, ts int64, stepMult int) {
		cum := 0
		for b := range numBuckets {
			cum += (b%3 + 1) * stepMult
			lset := baseLabels(metricPrefix+"_bucket", i, labels.BucketLabel, fmt.Sprintf("%d.0", b+1))
			_, err := app.Append(0, lset, ts, float64(cum))
			require.NoError(tb, err)
		}
		cum += stepMult
		_, err := app.Append(0, baseLabels(metricPrefix+"_bucket", i, labels.BucketLabel, "+Inf"), ts, float64(cum))
		require.NoError(tb, err)
		_, err = app.Append(0, baseLabels(metricPrefix+"_count", i), ts, float64(cum))
		require.NoError(tb, err)
		_, err = app.Append(0, baseLabels(metricPrefix+"_sum", i), ts, float64(cum)*1.25)
		require.NoError(tb, err)
	}

	makeNHCB := func(stepMult int) *histogram.Histogram {
		posBuckets := make([]int64, numBuckets+1)
		var prev int64
		var total uint64
		for b := range numBuckets {
			cnt := int64((b%3 + 1) * stepMult)
			posBuckets[b] = cnt - prev
			prev = cnt
			total += uint64(cnt)
		}
		infCnt := int64(stepMult)
		posBuckets[numBuckets] = infCnt - prev
		total += uint64(infCnt)
		return &histogram.Histogram{
			Schema:          histogram.CustomBucketsSchema,
			Count:           total,
			Sum:             float64(total) * 1.25,
			CustomValues:    customValues,
			PositiveSpans:   []histogram.Span{{Offset: 0, Length: uint32(numBuckets + 1)}},
			PositiveBuckets: posBuckets,
		}
	}

	makeNHE := func(stepMult int) *histogram.Histogram {
		posBuckets := make([]int64, numBuckets)
		var prev int64
		var total uint64
		for b := range numBuckets {
			cnt := int64((b%3 + 1) * stepMult)
			posBuckets[b] = cnt - prev
			prev = cnt
			total += uint64(cnt)
		}
		return &histogram.Histogram{
			Schema:          2,
			ZeroThreshold:   0.001,
			Count:           total,
			Sum:             float64(total) * 1.25,
			PositiveSpans:   []histogram.Span{{Offset: 0, Length: uint32(numBuckets)}},
			PositiveBuckets: posBuckets,
		}
	}

	for s := range numSamples {
		app := st.Appender(ctx)
		ts := int64(s) * 60000
		stepMult := s + 1
		nhcb := makeNHCB(stepMult)
		nhe := makeNHE(stepMult)

		for i := range numSeries {
			// 1. Classic only
			appendClassic(app, "bench_classic", i, ts, stepMult)

			// 2. NHCB only
			_, err := app.AppendHistogram(0, baseLabels("bench_nhcb", i), ts, nhcb, nil)
			require.NoError(tb, err)

			// 3. NHE only
			_, err = app.AppendHistogram(0, baseLabels("bench_nhe", i), ts, nhe, nil)
			require.NoError(tb, err)

			// 4. Migrated at midSample (Classic -> NHCB)
			if s < midSample {
				appendClassic(app, "bench_migrated", i, ts, stepMult)
			} else {
				if s == midSample {
					for b := range numBuckets {
						lset := baseLabels("bench_migrated_bucket", i, labels.BucketLabel, fmt.Sprintf("%d.0", b+1))
						_, err := app.Append(0, lset, ts, staleFloat)
						require.NoError(tb, err)
					}
					_, err := app.Append(0, baseLabels("bench_migrated_bucket", i, labels.BucketLabel, "+Inf"), ts, staleFloat)
					require.NoError(tb, err)
					_, err = app.Append(0, baseLabels("bench_migrated_count", i), ts, staleFloat)
					require.NoError(tb, err)
					_, err = app.Append(0, baseLabels("bench_migrated_sum", i), ts, staleFloat)
					require.NoError(tb, err)
				}
				_, err := app.AppendHistogram(0, baseLabels("bench_migrated", i), ts, nhcb, nil)
				require.NoError(tb, err)
			}

			// 5. Overlap (both Classic and NHCB stored at every timestamp)
			appendClassic(app, "bench_overlap", i, ts, stepMult)
			_, err = app.AppendHistogram(0, baseLabels("bench_overlap", i), ts, nhcb, nil)
			require.NoError(tb, err)

			// 6. Regular counter
			_, err = app.Append(0, baseLabels("bench_requests_total", i), ts, float64(stepMult*10))
			require.NoError(tb, err)
		}
		require.NoError(tb, app.Commit())
	}
}

// BenchmarkQuerier_RealTSDB benchmarks storage.Querier.Select + full sample
// iteration over a real TSDB Head for a 5m window (6 samples per series).
func BenchmarkQuerier_RealTSDB(b *testing.B) {
	const (
		numSeries  = 200
		numBuckets = 20
		numSamples = 12
	)
	st := teststorage.New(b)
	populateBenchTSDB(b, st, numSeries, numBuckets, numSamples)

	mint := int64(6 * 60000)
	maxt := int64(11 * 60000)

	eq := func(name, val string) *labels.Matcher {
		return labels.MustNewMatcher(labels.MatchEqual, name, val)
	}

	cases := []struct {
		name        string
		convertFrom []histogramconv.Representation
		matchers    []*labels.Matcher
	}{
		{
			name:        "StoredClassic_SelectBucket_ConvOff",
			convertFrom: nil,
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_classic_bucket")},
		},
		{
			name:        "StoredClassic_SelectBucket_ConvNHCB_NoOp",
			convertFrom: []histogramconv.Representation{histogramconv.NHCB},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_classic_bucket")},
		},
		{
			name:        "StoredNHCB_SelectBucket_ConvNHCB",
			convertFrom: []histogramconv.Representation{histogramconv.NHCB},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_nhcb_bucket")},
		},
		{
			name:        "StoredNHCB_SelectBucketWithLe_ConvNHCB",
			convertFrom: []histogramconv.Representation{histogramconv.NHCB},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_nhcb_bucket"), eq(labels.BucketLabel, "5.0")},
		},
		{
			name:        "StoredNHCB_SelectCount_ConvNHCB",
			convertFrom: []histogramconv.Representation{histogramconv.NHCB},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_nhcb_count")},
		},
		{
			name:        "StoredNHE_SelectBucket_ConvNHE",
			convertFrom: []histogramconv.Representation{histogramconv.NHE},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_nhe_bucket")},
		},
		{
			name:        "StoredNHCB_SelectNative_ConvOff",
			convertFrom: nil,
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_nhcb")},
		},
		{
			name:        "StoredNHCB_SelectNative_ConvClassic_NoOp",
			convertFrom: []histogramconv.Representation{histogramconv.Classic},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_nhcb")},
		},
		{
			name:        "StoredClassic_SelectNative_ConvClassic",
			convertFrom: []histogramconv.Representation{histogramconv.Classic},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_classic")},
		},
		{
			name:        "StoredOverlap_SelectBucket_ConvNHCB",
			convertFrom: []histogramconv.Representation{histogramconv.NHCB},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_overlap_bucket")},
		},
		{
			name:        "StoredOverlap_SelectBucketWithLe_ConvNHCB",
			convertFrom: []histogramconv.Representation{histogramconv.NHCB},
			matchers:    []*labels.Matcher{eq(model.MetricNameLabel, "bench_overlap_bucket"), eq(labels.BucketLabel, "5.0")},
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var it chunkenc.Iterator
			for b.Loop() {
				rawQ, err := st.Querier(mint, maxt)
				if err != nil {
					b.Fatal(err)
				}
				var q storage.Querier = rawQ
				if len(tc.convertFrom) > 0 {
					q = histogramconv.NewQuerier(rawQ, tc.convertFrom)
				}
				ss := q.Select(context.Background(), false, &storage.SelectHints{Start: mint, End: maxt}, tc.matchers...)
				seriesCount := 0
				for ss.Next() {
					seriesCount++
					s := ss.At()
					_ = s.Labels()
					it = s.Iterator(it)
					for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
						switch vt {
						case chunkenc.ValFloat:
							_, _ = it.At()
						case chunkenc.ValHistogram:
							_, _ = it.AtHistogram(nil)
						case chunkenc.ValFloatHistogram:
							_, _ = it.AtFloatHistogram(nil)
						}
					}
					if err := it.Err(); err != nil {
						b.Fatal(err)
					}
				}
				if err := ss.Err(); err != nil {
					b.Fatal(err)
				}
				if seriesCount == 0 {
					b.Fatal("expected series, got 0")
				}
				_ = q.Close()
			}
		})
	}
}

// BenchmarkPromQL_EndToEnd benchmarks full PromQL query evaluation (both
// instant and 1h range queries) across classic, NHCB, NHE, and mixed storage.
func BenchmarkPromQL_EndToEnd(b *testing.B) {
	const (
		numSeries  = 200
		numSamples = 120 // 2h at 1m step
	)
	for _, numBuckets := range []int{10, 30} {
		b.Run(fmt.Sprintf("buckets=%d", numBuckets), func(b *testing.B) {
			st := teststorage.New(b)
			populateBenchTSDB(b, st, numSeries, numBuckets, numSamples)

			evalInstantTime := time.Unix(int64((numSamples-1)*60), 0)
			rangeStart := time.Unix(int64(30*60), 0)
			rangeEnd := time.Unix(int64(90*60), 0) // 1h range covering migration midpoint at 60m
			rangeStep := time.Minute

			cases := []struct {
				name        string
				convertFrom []histogramconv.Representation
				query       string
			}{
				// Quantile queries
				{
					name:        "Quantile/StoredClassic_QueryClassic_ConvOff",
					convertFrom: nil,
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_classic_bucket[5m])))`,
				},
				{
					name:        "Quantile/StoredClassic_QueryClassic_ConvNHCB_NoOp",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_classic_bucket[5m])))`,
				},
				{
					name:        "Quantile/StoredClassic_QueryClassic_ConvNHCB_OptOutMatcher",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_classic_bucket{__convert_stored_as__=""}[5m])))`,
				},
				{
					name:        "Quantile/StoredClassic_QueryClassic_ConvNHCB_ClassicFilterMatcher",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_classic_bucket{__convert_stored_as__="classic"}[5m])))`,
				},
				{
					name:        "Quantile/StoredNHCB_QueryNative_ConvOff",
					convertFrom: nil,
					query:       `histogram_quantile(0.99, sum(rate(bench_nhcb[5m])))`,
				},
				{
					name:        "Quantile/StoredNHCB_QueryNative_ConvClassic_NoOp",
					convertFrom: []histogramconv.Representation{histogramconv.Classic},
					query:       `histogram_quantile(0.99, sum(rate(bench_nhcb[5m])))`,
				},
				{
					name:        "Quantile/StoredNHCB_QueryClassic_ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_nhcb_bucket[5m])))`,
				},
				{
					name:        "Quantile/StoredClassic_QueryNative_ConvClassic",
					convertFrom: []histogramconv.Representation{histogramconv.Classic},
					query:       `histogram_quantile(0.99, sum(rate(bench_classic[5m])))`,
				},
				{
					name:        "Quantile/StoredNHE_QueryNative_ConvOff",
					convertFrom: nil,
					query:       `histogram_quantile(0.99, sum(rate(bench_nhe[5m])))`,
				},
				{
					name:        "Quantile/StoredNHE_QueryClassic_ConvNHE",
					convertFrom: []histogramconv.Representation{histogramconv.NHE},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_nhe_bucket[5m])))`,
				},
				{
					name:        "Quantile/StoredMigrated50_50_QueryClassic_ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_migrated_bucket[5m])))`,
				},
				{
					name:        "Quantile/StoredMigrated50_50_QueryNative_ConvClassic",
					convertFrom: []histogramconv.Representation{histogramconv.Classic},
					query:       `histogram_quantile(0.99, sum(rate(bench_migrated[5m])))`,
				},
				{
					name:        "Quantile/StoredOverlap_QueryClassic_ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `histogram_quantile(0.99, sum by (le) (rate(bench_overlap_bucket[5m])))`,
				},

				// Count rate queries
				{
					name:        "CountRate/StoredClassic_QueryClassic_ConvOff",
					convertFrom: nil,
					query:       `sum(rate(bench_classic_count[5m]))`,
				},
				{
					name:        "CountRate/StoredNHCB_QueryNative_ConvOff",
					convertFrom: nil,
					query:       `histogram_count(sum(rate(bench_nhcb[5m])))`,
				},
				{
					name:        "CountRate/StoredNHCB_QueryClassic_ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `sum(rate(bench_nhcb_count[5m]))`,
				},
				{
					name:        "CountRate/StoredClassic_QueryNative_ConvClassic",
					convertFrom: []histogramconv.Representation{histogramconv.Classic},
					query:       `histogram_count(sum(rate(bench_classic[5m])))`,
				},

				// Single bucket le filter queries
				{
					name:        "SingleBucketLe/StoredClassic_ConvOff",
					convertFrom: nil,
					query:       `sum(rate(bench_classic_bucket{le="5.0"}[5m]))`,
				},
				{
					name:        "SingleBucketLe/StoredNHCB_ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `sum(rate(bench_nhcb_bucket{le="5.0"}[5m]))`,
				},
				{
					name:        "SingleBucketLe/StoredOverlap_ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `sum(rate(bench_overlap_bucket{le="5.0"}[5m]))`,
				},

				// Non-histogram counter queries (collateral overhead check)
				{
					name:        "Counter/ConvOff",
					convertFrom: nil,
					query:       `sum(rate(bench_requests_total[5m]))`,
				},
				{
					name:        "Counter/ConvNHCB",
					convertFrom: []histogramconv.Representation{histogramconv.NHCB},
					query:       `sum(rate(bench_requests_total[5m]))`,
				},
				{
					name:        "Counter/ConvClassic",
					convertFrom: []histogramconv.Representation{histogramconv.Classic},
					query:       `sum(rate(bench_requests_total[5m]))`,
				},
			}

			for _, tc := range cases {
				engine := promql.NewEngine(promql.EngineOpts{
					MaxSamples:              50000000,
					Timeout:                 100 * time.Second,
					EnableAtModifier:        true,
					EnableNegativeOffset:    true,
					HistogramConversionFrom: tc.convertFrom,
				})

				b.Run("eval=instant/"+tc.name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						qry, err := engine.NewInstantQuery(context.Background(), st, nil, tc.query, evalInstantTime)
						if err != nil {
							b.Fatal(err)
						}
						res := qry.Exec(context.Background())
						if res.Err != nil {
							b.Fatal(res.Err)
						}
						qry.Close()
					}
				})

				b.Run("eval=range1h/"+tc.name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						qry, err := engine.NewRangeQuery(context.Background(), st, nil, tc.query, rangeStart, rangeEnd, rangeStep)
						if err != nil {
							b.Fatal(err)
						}
						res := qry.Exec(context.Background())
						if res.Err != nil {
							b.Fatal(res.Err)
						}
						qry.Close()
					}
				})
			}
		})
	}
}

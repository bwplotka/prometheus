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

//go:build demo

package main

import (
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	// demo_slo_request_duration_seconds simulates a service with a 100ms (0.1s)
	// latency SLO where every request completes in 90ms-99ms (always < 100ms).
	//
	// Classic histograms and NHCB have an explicit bucket boundary at 0.1s
	// (from prometheus.DefBuckets), so le="0.1" is always 100% accurate (1.0).
	// In exponential native histograms (NHE, schema 3 with factor 1.1), 0.1s is
	// not a power-of-two boundary and falls 42.5% of the way through bucket
	// (96.39ms, 105.11ms], causing NHE to report only ~42.5% of requests <= 0.1s
	// whenever latencies sit between 96.4ms and 99ms.
	sloHist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:                        "demo_slo_request_duration_seconds",
		Help:                        "[DEMO] Simulated request duration for a service with a 100ms (0.1s) latency SLO, always completing between 90ms and 99ms.",
		Buckets:                     prometheus.DefBuckets,
		NativeHistogramBucketFactor: 1.1,
	})
	prometheus.MustRegister(sloHist)

	start := time.Now()
	observeBatch := func() {
		const period = 6 * time.Minute
		phase := 2 * math.Pi * float64(time.Since(start)) / float64(period)
		// Oscillate the batch center between 91ms (in schema-3 bucket
		// (88.39ms, 96.39ms]) and 98ms (in schema-3 bucket (96.39ms, 105.11ms]),
		// starting at 98ms so the interpolation error is immediately visible.
		center := 0.0945 + 0.0035*math.Cos(phase)
		for i := range 20 {
			offset := (float64(i)/19.0 - 0.5) * 0.002 // +-1ms spread -> [90ms, 99ms]
			sloHist.Observe(center + offset)
		}
	}
	observeBatch()
	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			observeBatch()
		}
	}()
}

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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	e2einteractive "github.com/efficientgo/e2e/interactive"
	"github.com/stretchr/testify/require"
)

// TestMain_PromQLCompatDemo is an interactive demo of native-to-classic
// histogram conversion (NHCB and exponential), enabled with
// --enable-feature=promql-nhcb-as-classic.
//
// Prometheus scrapes itself with 5 jobs (see demo_histcompat.yaml), each
// storing its prometheus_http_request_duration_seconds and
// demo_slo_request_duration_seconds histograms differently:
//
//   - classic: classic histogram only.
//   - nhcb: native histogram with custom buckets (NHCB) only.
//   - classic-and-nhcb: both a classic histogram and NHCB.
//   - native: native histogram with exponential buckets (NHE) only.
//   - classic-and-native: both a classic and a native histogram with
//     exponential buckets (NHE).
//
// Every browser tab compares a classic histogram query (top) with its native
// histogram equivalent (bottom), like the landing page of
// demo_histcompat_gce/deploy.sh:
//
//  1. What is stored: __opt_classic_from="none" turns the conversion off so
//     the classic query only returns classic, classic-and-nhcb, and
//     classic-and-native, while the native query returns the stored NHCB and
//     exponential histograms with their bucket distributions.
//  2. The request rates with the conversion on: __opt_classic_from="debug"
//     adds __stored_as__ ("classic", "nhcb", or "nhe") and returns stored and
//     converted series side by side. Without "debug", stored classic data
//     shadows converted series so dual-scraped jobs are not counted twice.
//  3. The 99th percentile across all 5 jobs in classic and native forms.
//  4. The bucket series converted from NHCB and NHE next to the native
//     histograms.
//  5. The share of requests faster than 100ms using le="0.1" vs
//     histogram_fraction.
//  6. The 100ms (le="0.1") SLO interpolation error on
//     demo_slo_request_duration_seconds (requests oscillating between 90ms and
//     99ms, always < 100ms): classic and nhcb stay flat at 1.0 (100%) because
//     0.1s is an explicit custom bucket boundary, whereas nhe drops to ~0.425
//     (42.5%) whenever latencies enter the schema-3 exponential bucket
//     (96.39ms, 105.11ms] that straddles 0.1s.
//  7. The 99th percentile on demo_slo_request_duration_seconds vs the 100ms
//     SLO: classic and nhcb stay at 99.5ms (<= 100ms), native schema-3 nhe
//     falsely crosses 100ms to 105.0ms when latencies enter (96.39ms, 105.11ms],
//     and classic-converted nhe (reduced to schema 2 (88.39ms, 105.11ms]) stays
//     at 104.9ms throughout.
//
// See docs/feature_flags.md for details. The UI assets have to be built once
// before, e.g. with `make assets`.
//
// Start Prometheus on localhost:1234 (runs until interrupted and opens 7
// browser tabs):
//
//	go test -tags demo -timeout 0 -v -run TestMain_PromQLCompatDemo ./cmd/prometheus
func TestMain_PromQLCompatDemo(t *testing.T) {
	dir, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir("../../") // Ensure UI is sourced.

	go openDemoTabs(t, "1234")

	os.Args = []string{
		"main",
		"--web.listen-address=0.0.0.0:1234",
		"--config.file=" + filepath.Join(dir, "demo_histcompat.yaml"),
		"--storage.tsdb.path=" + filepath.Join(dir, "data", "histcompat"),
		"--enable-feature=promql-nhcb-as-classic",
	}
	main()
}

// openDemoTabs opens browser tabs comparing classic histogram queries (g0) with
// their native histogram equivalents (g1), once Prometheus had some time to
// start and scrape itself. Keep in sync with demo_pages in
// demo_histcompat_gce/deploy.sh.
func openDemoTabs(t *testing.T, port string) {
	time.Sleep(10 * time.Second)

	for _, tab := range []struct {
		view, classic, native string
	}{
		{
			view:    "table",
			classic: `sum by (job) (rate(prometheus_http_request_duration_seconds_count{__opt_classic_from="none"}[5m]))`,
			native:  `sum by (job) (prometheus_http_request_duration_seconds)`,
		},
		{
			view:    "table",
			classic: `sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds_count{__opt_classic_from="debug"}[5m]))`,
			native:  `histogram_count(sum by (job) (rate(prometheus_http_request_duration_seconds[5m])))`,
		},
		{
			view:    "graph",
			classic: `histogram_quantile(0.99, sum by (job, le) (rate(prometheus_http_request_duration_seconds_bucket[5m])))`,
			native:  `histogram_quantile(0.99, sum by (job) (rate(prometheus_http_request_duration_seconds[5m])))`,
		},
		{
			view:    "table",
			classic: `sum by (job, le) (rate(prometheus_http_request_duration_seconds_bucket[5m]))`,
			native:  `sum by (job) (rate(prometheus_http_request_duration_seconds[5m]))`,
		},
		{
			view:    "table",
			classic: `sum by (job) (rate(prometheus_http_request_duration_seconds_bucket{le="0.1"}[5m])) / sum by (job) (rate(prometheus_http_request_duration_seconds_count[5m]))`,
			native:  `histogram_fraction(0, 0.1, sum by (job) (rate(prometheus_http_request_duration_seconds[5m])))`,
		},
		{
			view:    "graph",
			classic: `sum by (job) (rate(demo_slo_request_duration_seconds_bucket{le="0.1"}[1m])) / sum by (job) (rate(demo_slo_request_duration_seconds_count[1m]))`,
			native:  `histogram_fraction(0, 0.1, sum by (job) (rate(demo_slo_request_duration_seconds[1m])))`,
		},
		{
			view:    "graph",
			classic: `histogram_quantile(0.99, sum by (job, le) (rate(demo_slo_request_duration_seconds_bucket[1m])))`,
			native:  `histogram_quantile(0.99, sum by (job) (rate(demo_slo_request_duration_seconds[1m])))`,
		},
	} {
		params := url.Values{}
		for i, expr := range []string{tab.classic, tab.native} {
			params.Set(fmt.Sprintf("g%d.expr", i), expr)
			params.Set(fmt.Sprintf("g%d.tab", i), tab.view)
		}
		if err := e2einteractive.OpenInBrowser("http://localhost:" + port + "/query?" + params.Encode()); err != nil {
			t.Log("Failed to open browser tab:", err)
		}
	}
}

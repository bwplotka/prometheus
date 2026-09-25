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

// TestMain_PromQLCompatDemo is an interactive demo of query-time histogram
// conversion, enabled with --enable-feature=promql-histogram-conversion and
// --query.convert-histograms-from=nhcb,nhe,classic.
//
// Prometheus scrapes itself with 4 jobs (see demo_histcompat.yaml), each
// storing its prometheus_http_request_duration_seconds histogram differently:
//
//   - classic: classic histogram only.
//   - nhcb: native histogram with custom buckets (NHCB) only.
//   - native: native histogram with exponential buckets only.
//   - classic-and-native: both a classic and a native histogram with
//     exponential buckets.
//
// Every browser tab compares a classic histogram query (top) with its native
// histogram equivalent (bottom), like the landing page of
// demo_histcompat_gce/deploy.sh:
//
//  1. What is stored: __convert_stored_as__="" turns the conversion off and
//     __debug_stored_as__="true" adds the representation each result is read
//     from, so each query only returns the jobs storing its representation,
//     the native one with the bucket distributions of the stored histograms.
//  2. The request rates with the conversion on: both forms return all jobs.
//     For classic-and-native, stored data wins over converted data, so each
//     query reads the histogram stored in its own form.
//  3. The 99th percentile, which differs with the stored buckets.
//  4. The buckets. Buckets converted from exponential histograms have derived
//     le boundaries, which depend on the selected series and time range.
//  5. The share of requests faster than 100ms: le="0.1" selects nothing for
//     the native job, as 0.1 is not an exponential bucket boundary, while
//     histogram_fraction works for all jobs.
//
// See docs/feature_flags.md for details. The UI assets have to be built once
// before, e.g. with `make assets`.
//
// Start Prometheus on localhost:1234 (runs until interrupted and opens 5
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
		"--enable-feature=promql-histogram-conversion",
		"--query.convert-histograms-from=nhcb,nhe,classic",
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
			classic: `sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds_count{__convert_stored_as__="", __debug_stored_as__="true"}[5m]))`,
			native:  `sum by (job, __stored_as__) (prometheus_http_request_duration_seconds{__convert_stored_as__="", __debug_stored_as__="true"})`,
		},
		{
			view:    "table",
			classic: `sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds_count{__debug_stored_as__="true"}[5m]))`,
			native:  `histogram_count(sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds{__debug_stored_as__="true"}[5m])))`,
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

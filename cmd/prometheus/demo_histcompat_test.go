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

// TestMain_PromQLCompatDemo is an interactive demo of the PromQL histogram
// compatibility layer, enabled with --enable-feature=promql-nh-classic-compat.
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
// histogram equivalent (bottom). Both return the classic, nhcb and native jobs,
// as the compatibility layer converts the histograms stored in the other
// representation. For classic-and-native, the stored and the converted series
// are both returned: the classic histogram queries count it twice, the native
// histogram ones drop it with a warning, see the limitations in
// docs/feature_flags.md.
//
// The UI assets have to be built once before, e.g. with `make assets`.
//
// Start Prometheus on localhost:1234 (runs until interrupted and opens 3
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
		"--enable-feature=promql-nh-classic-compat",
	}
	main()
}

// openDemoTabs opens browser tabs comparing classic histogram queries (g0) with
// their native histogram equivalents (g1), once Prometheus had some time to
// start and scrape itself.
func openDemoTabs(t *testing.T, port string) {
	time.Sleep(10 * time.Second)

	for _, tab := range []struct {
		view, classic, native string
	}{
		{
			view:    "table",
			classic: `sum(prometheus_http_request_duration_seconds_bucket) by (job, le)`,
			native:  `sum(prometheus_http_request_duration_seconds) by (job)`,
		},
		{
			view:    "graph",
			classic: `histogram_quantile(0.99, sum(prometheus_http_request_duration_seconds_bucket) by (job, le))`,
			native:  `histogram_quantile(0.99, sum(prometheus_http_request_duration_seconds) by (job))`,
		},
		{
			view:    "graph",
			classic: `sum(prometheus_http_request_duration_seconds_count) by (job)`,
			native:  `histogram_count(sum(prometheus_http_request_duration_seconds) by (job))`,
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

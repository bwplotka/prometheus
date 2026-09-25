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

// TestMain_PromQLCompatDemo is an interactive demo of both PromQL histogram
// compatibility layers: promql-nhcb-as-classic and promql-classic-as-nhcb.
//
// Every Prometheus scrapes itself twice (see demo_histcompat_*.yaml):
// job="self" stores prometheus_http_request_duration_seconds as a classic
// histogram, job="self-nh" stores the same histogram as NHCB. Every browser tab
// compares a classic histogram query (top) with its native histogram
// equivalent (bottom):
//
//   - normal: classic queries only see "self", native queries only "self-nh".
//   - nhcb-as-classic: classic queries see both jobs, native only "self-nh".
//   - dual (both flags): classic and native queries see both jobs.
//
// The UI assets have to be built once before, e.g. with `make assets`.
//
// Recommended way, each Prometheus in a separate terminal (every command runs
// until interrupted and opens 3 browser tabs):
//
//   - Start Prom (localhost:1234):
//     go test -tags demo -timeout 0 -v -run "TestMain_PromQLCompatDemo/normal" ./cmd/prometheus
//
//   - Start Prom with --enable-feature=promql-nhcb-as-classic (localhost:1235):
//     go test -tags demo -timeout 0 -v -run "TestMain_PromQLCompatDemo/nhcb-as-classic" ./cmd/prometheus
//
//   - Start Prom with --enable-feature=promql-nhcb-as-classic,promql-classic-as-nhcb (localhost:1236):
//     go test -tags demo -timeout 0 -v -run "TestMain_PromQLCompatDemo/dual" ./cmd/prometheus
func TestMain_PromQLCompatDemo(t *testing.T) {
	dir, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir("../../") // Ensure UI is sourced.

	for _, tc := range []struct {
		name, port, config, features string
	}{
		{name: "normal", port: "1234", config: "demo_histcompat_normal.yaml"},
		{name: "nhcb-as-classic", port: "1235", config: "demo_histcompat_nhcb_as_classic.yaml", features: "promql-nhcb-as-classic"},
		{name: "dual", port: "1236", config: "demo_histcompat_dual.yaml", features: "promql-nhcb-as-classic,promql-classic-as-nhcb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			go openDemoTabs(t, tc.port)

			os.Args = []string{
				"main",
				"--web.listen-address=0.0.0.0:" + tc.port,
				"--config.file=" + filepath.Join(dir, tc.config),
				"--storage.tsdb.path=" + filepath.Join(dir, "data", tc.name),
			}
			if tc.features != "" {
				os.Args = append(os.Args, "--enable-feature="+tc.features)
			}
			main()
		})
	}
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

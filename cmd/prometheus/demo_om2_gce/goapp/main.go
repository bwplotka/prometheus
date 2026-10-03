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

// [DEMO] Example Go application instrumented with client_golang that serves
// OpenMetrics 1.0 or 2.0 on /metrics, depending on the Accept header. It
// exposes the same kind of metrics as the Java example (../javaapp), so that
// the OpenMetrics 1.0 and 2.0 scrapes of both can be compared.
package main

import (
	"flag"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

func main() {
	addr := flag.String("listen-address", "127.0.0.1:8081", "Address to serve /metrics on.")
	flag.Parse()

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "app_build_info",
		Help: "Application build information.",
	}, []string{"version", "sdk"})
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests handled.",
	}, []string{"method", "status"})
	requestBytes := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "http_request_size_bytes_total",
		Help: "Total HTTP request bytes received.",
		Unit: "bytes",
	})
	// NOTE: A dotted, OpenTelemetry style name and label, which needs UTF-8
	// support (quoting) in both OpenMetrics versions.
	inFlight := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "http.server.active_requests",
		Help: "Current number of in-flight HTTP requests (OTel-style dotted name).",
	}, []string{"service.name"})
	// A histogram with both classic and native buckets. OpenMetrics 1.0 can only
	// carry the classic buckets, OpenMetrics 2.0 carries both in one composite
	// sample.
	requestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:                        "http_request_duration_seconds",
		Help:                        "HTTP request duration in seconds.",
		Unit:                        "seconds",
		Buckets:                     []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5},
		NativeHistogramBucketFactor: 1.1,
	}, []string{"method"})
	rpcLatency := prometheus.NewSummary(prometheus.SummaryOpts{
		Name:       "rpc_latency_seconds",
		Help:       "RPC latency summary in seconds.",
		Unit:       "seconds",
		Objectives: map[float64]float64{0.5: 0.01, 0.9: 0.01, 0.99: 0.001},
	})
	reg.MustRegister(buildInfo, requests, requestBytes, inFlight, requestDuration, rpcLatency)

	exemplar := prometheus.Labels{"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736", "span_id": "00f067aa0ba902b7"}

	// Seed the same initial observations as the Java example.
	buildInfo.WithLabelValues("1.0.0", "client_golang").Set(1)
	requests.WithLabelValues("GET", "200").(prometheus.ExemplarAdder).AddWithExemplar(5, exemplar)
	requests.WithLabelValues("POST", "201").Add(2)
	requestBytes.(prometheus.ExemplarAdder).AddWithExemplar(4096, exemplar)
	inFlight.WithLabelValues("demo-api").Set(3)
	requestDuration.WithLabelValues("GET").(prometheus.ExemplarObserver).ObserveWithExemplar(0.042, exemplar)
	requestDuration.WithLabelValues("GET").Observe(0.18)
	requestDuration.WithLabelValues("GET").Observe(0.73)
	rpcLatency.Observe(0.015)
	rpcLatency.Observe(0.085)

	go func() {
		for range time.Tick(2 * time.Second) {
			requests.WithLabelValues("GET", "200").(prometheus.ExemplarAdder).AddWithExemplar(1, exemplar)
			requestBytes.(prometheus.ExemplarAdder).AddWithExemplar(512, exemplar)
			requestDuration.WithLabelValues("GET").(prometheus.ExemplarObserver).ObserveWithExemplar(0.025+rand.Float64()*0.2, exemplar)
			rpcLatency.Observe(0.01 + rand.Float64()*0.1)
		}
	}()

	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		// NOTE: EnableOpenMetricsTextCreatedSamples stays off, so OM1 has no
		// extra _created series. OM2 still carries start timestamps as st@, as
		// the OM2 encoder writes them regardless of this option.
		EnableOpenMetrics: true,
		// OpenMetrics 2.0 is experimental, so it has to be listed explicitly.
		AcceptedFormats: []expfmt.Format{
			expfmt.FmtOpenMetrics_2_0_0,
			expfmt.FmtOpenMetrics_1_0_0,
			expfmt.FmtText,
		},
	}))
	log.Printf("Serving client_golang metrics on http://%s/metrics", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

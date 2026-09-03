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

package remote

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang/snappy"
	config_util "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/promslog"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"

	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/metadata"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/util/compression"
)

func TestBuildOTLPWriteRequest_Samples(t *testing.T) {
	batch := []timeSeries{
		{
			seriesLabels: labels.FromStrings(
				labels.MetricName, "http_requests_total",
				"job", "api-server",
				"instance", "host1:8080",
				"method", "GET",
				"status", "200",
			),
			value:          123,
			timestamp:      1000,
			startTimestamp: 500,
			sType:          tSample,
			metadata: &metadata.Metadata{
				Type: model.MetricTypeCounter,
				Help: "Total HTTP requests",
				Unit: "1",
			},
		},
		{
			seriesLabels: labels.FromStrings(
				labels.MetricName, "memory_usage_bytes",
				"job", "api-server",
				"instance", "host1:8080",
				"type", "heap",
			),
			value:     1024 * 1024,
			timestamp: 1000,
			sType:     tSample,
			metadata: &metadata.Metadata{
				Type: model.MetricTypeGauge,
				Help: "Current memory usage in bytes",
				Unit: "bytes",
			},
		},
	}

	compressed, highest, lowest, sampleCount, exemplarCount, histogramCount, metadataCount, err := buildOTLPWriteRequest(
		promslog.NewNopLogger(),
		batch,
		true,
		true,
		nil,
		nil,
		compression.NewSyncEncodeBuffer(),
		compression.Snappy,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1000), highest)
	require.Equal(t, int64(1000), lowest)
	require.Equal(t, 2, sampleCount)
	require.Equal(t, 0, exemplarCount)
	require.Equal(t, 0, histogramCount)
	require.Equal(t, 2, metadataCount)

	decompressed, err := snappy.Decode(nil, compressed)
	require.NoError(t, err)

	otlpReq := pmetricotlp.NewExportRequest()
	err = otlpReq.UnmarshalProto(decompressed)
	require.NoError(t, err)

	metrics := otlpReq.Metrics()
	require.Equal(t, 1, metrics.ResourceMetrics().Len())

	rm := metrics.ResourceMetrics().At(0)
	serviceName, ok := rm.Resource().Attributes().Get("service.name")
	require.True(t, ok)
	require.Equal(t, "api-server", serviceName.AsString())

	instanceID, ok := rm.Resource().Attributes().Get("service.instance.id")
	require.True(t, ok)
	require.Equal(t, "host1:8080", instanceID.AsString())

	require.Equal(t, 1, rm.ScopeMetrics().Len())
	sm := rm.ScopeMetrics().At(0)
	require.Equal(t, "prometheus", sm.Scope().Name())

	require.Equal(t, 2, sm.Metrics().Len())

	// Metric 1: Counter Sum
	m1 := sm.Metrics().At(0)
	require.Equal(t, "http_requests_total", m1.Name())
	require.Equal(t, "Total HTTP requests", m1.Description())
	require.Equal(t, "1", m1.Unit())
	require.Equal(t, pmetric.MetricTypeSum, m1.Type())
	require.True(t, m1.Sum().IsMonotonic())
	require.Equal(t, pmetric.AggregationTemporalityCumulative, m1.Sum().AggregationTemporality())
	require.Equal(t, 1, m1.Sum().DataPoints().Len())
	dp1 := m1.Sum().DataPoints().At(0)
	require.Equal(t, 123.0, dp1.DoubleValue())
	require.Equal(t, uint64(1000*time.Millisecond), uint64(dp1.Timestamp()))
	require.Equal(t, uint64(500*time.Millisecond), uint64(dp1.StartTimestamp()))
	method, ok := dp1.Attributes().Get("method")
	require.True(t, ok)
	require.Equal(t, "GET", method.AsString())

	// Metric 2: Gauge
	m2 := sm.Metrics().At(1)
	require.Equal(t, "memory_usage_bytes", m2.Name())
	require.Equal(t, "Current memory usage in bytes", m2.Description())
	require.Equal(t, "bytes", m2.Unit())
	require.Equal(t, pmetric.MetricTypeGauge, m2.Type())
	require.Equal(t, 1, m2.Gauge().DataPoints().Len())
	dp2 := m2.Gauge().DataPoints().At(0)
	require.Equal(t, float64(1024*1024), dp2.DoubleValue())
	require.Equal(t, uint64(1000*time.Millisecond), uint64(dp2.Timestamp()))
}

func TestBuildOTLPWriteRequest_NativeHistogram(t *testing.T) {
	h := &histogram.Histogram{
		Count:         15,
		Sum:           42.5,
		Schema:        1,
		ZeroThreshold: 0.001,
		ZeroCount:     2,
		PositiveSpans: []histogram.Span{
			{Offset: 0, Length: 2},
		},
		PositiveBuckets: []int64{3, 4}, // 3, 3+4=7
	}

	batch := []timeSeries{
		{
			seriesLabels: labels.FromStrings(
				labels.MetricName, "request_duration_seconds",
				"job", "web",
				"instance", "node-1",
			),
			histogram:      h,
			timestamp:      2000,
			startTimestamp: 1000,
			sType:          tHistogram,
			metadata: &metadata.Metadata{
				Type: model.MetricTypeHistogram,
				Help: "Duration of requests in seconds",
				Unit: "seconds",
			},
		},
	}

	compressed, _, _, sampleCount, _, histogramCount, _, err := buildOTLPWriteRequest(
		promslog.NewNopLogger(),
		batch,
		true,
		true,
		nil,
		nil,
		compression.NewSyncEncodeBuffer(),
		compression.Snappy,
	)
	require.NoError(t, err)
	require.Equal(t, 0, sampleCount)
	require.Equal(t, 1, histogramCount)

	decompressed, err := snappy.Decode(nil, compressed)
	require.NoError(t, err)

	otlpReq := pmetricotlp.NewExportRequest()
	err = otlpReq.UnmarshalProto(decompressed)
	require.NoError(t, err)

	m := otlpReq.Metrics().ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0)
	require.Equal(t, "request_duration_seconds", m.Name())
	require.Equal(t, pmetric.MetricTypeExponentialHistogram, m.Type())

	eh := m.ExponentialHistogram()
	require.Equal(t, pmetric.AggregationTemporalityCumulative, eh.AggregationTemporality())
	require.Equal(t, 1, eh.DataPoints().Len())

	dp := eh.DataPoints().At(0)
	require.Equal(t, uint64(15), dp.Count())
	require.Equal(t, 42.5, dp.Sum())
	require.Equal(t, int32(1), dp.Scale())
	require.Equal(t, uint64(2), dp.ZeroCount())
	require.Equal(t, 0.001, dp.ZeroThreshold())
	require.Equal(t, int32(0), dp.Positive().Offset())
	require.Equal(t, 2, dp.Positive().BucketCounts().Len())
	require.Equal(t, uint64(3), dp.Positive().BucketCounts().At(0))
	require.Equal(t, uint64(7), dp.Positive().BucketCounts().At(1))
}

func TestClient_OTLPHeadersAndPayload(t *testing.T) {
	var (
		receivedContentType string
		receivedRwVersion   string
		receivedBody        []byte
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedRwVersion = r.Header.Get(RemoteWriteVersionHeader)
		body, err := io.ReadAll(r.Body)
		if err == nil {
			receivedBody = body
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	client, err := NewWriteClient("test_otlp", &ClientConfig{
		URL:           &config_util.URL{URL: serverURL},
		Timeout:       model.Duration(5 * time.Second),
		WriteProtoMsg: ProtobufMessageOTLP,
	})
	require.NoError(t, err)

	batch := []timeSeries{
		{
			seriesLabels: labels.FromStrings(
				labels.MetricName, "test_metric",
				"job", "test_job",
			),
			value:     42,
			timestamp: 1000,
			sType:     tSample,
		},
	}

	compressed, _, _, _, _, _, _, err := buildOTLPWriteRequest(
		promslog.NewNopLogger(),
		batch,
		false,
		false,
		nil,
		nil,
		compression.NewSyncEncodeBuffer(),
		compression.Snappy,
	)
	require.NoError(t, err)

	stats, err := client.Store(context.Background(), compressed, 0)
	require.NoError(t, err)
	_ = stats

	require.Equal(t, "application/x-protobuf", receivedContentType)
	require.Empty(t, receivedRwVersion, "OTLP export should not set X-Prometheus-Remote-Write-Version")

	decompressed, err := snappy.Decode(nil, receivedBody)
	require.NoError(t, err)

	req := pmetricotlp.NewExportRequest()
	err = req.UnmarshalProto(decompressed)
	require.NoError(t, err)
	require.Equal(t, 1, req.Metrics().MetricCount())
}

func TestQueueManager_OTLP(t *testing.T) {
	dir := t.TempDir()
	metrics := newQueueManagerMetrics(nil, "", "")
	cfg := testDefaultQueueConfig()
	mcfg := config.DefaultMetadataConfig

	receivedRequests := make(chan pmetricotlp.ExportRequest, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decompressed, err := snappy.Decode(nil, body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		otlpReq := pmetricotlp.NewExportRequest()
		if err := otlpReq.UnmarshalProto(decompressed); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedRequests <- otlpReq
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	client, err := NewWriteClient("test_otlp_qm", &ClientConfig{
		URL:           &config_util.URL{URL: serverURL},
		Timeout:       model.Duration(5 * time.Second),
		WriteProtoMsg: ProtobufMessageOTLP,
	})
	require.NoError(t, err)

	qm := NewQueueManager(metrics, nil, nil, nil, dir, newEWMARate(ewmaWeight, shardUpdateDuration), cfg, mcfg, labels.EmptyLabels(), nil, client, defaultFlushDeadline, newPool(), newHighestTimestampMetric(), nil, false, false, false, ProtobufMessageOTLP, record.NewBuffersPool())
	qm.Start()
	defer qm.Stop()

	series := []record.RefSeries{
		{
			Ref: 1,
			Labels: labels.FromStrings(
				labels.MetricName, "test_otlp_metric_total",
				"job", "test_job",
				"instance", "test_instance",
			),
		},
	}
	qm.StoreSeries(series, 1)

	samples := []record.RefSample{
		{
			Ref: 1,
			T:   1000,
			V:   123.45,
		},
	}
	qm.Append(samples)

	select {
	case req := <-receivedRequests:
		require.Equal(t, 1, req.Metrics().MetricCount())
		m := req.Metrics().ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0)
		require.Equal(t, "test_otlp_metric_total", m.Name())
		require.Equal(t, pmetric.MetricTypeSum, m.Type())
		require.Equal(t, 123.45, m.Sum().DataPoints().At(0).DoubleValue())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OTLP remote write request")
	}
}


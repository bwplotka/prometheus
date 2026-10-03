package io.prometheus.examples;

import io.prometheus.metrics.config.PrometheusProperties;
import io.prometheus.metrics.core.metrics.Counter;
import io.prometheus.metrics.core.metrics.Gauge;
import io.prometheus.metrics.core.metrics.Histogram;
import io.prometheus.metrics.core.metrics.Info;
import io.prometheus.metrics.core.metrics.Summary;
import io.prometheus.metrics.exporter.httpserver.HTTPServer;
import io.prometheus.metrics.instrumentation.jvm.JvmMetrics;
import io.prometheus.metrics.model.snapshots.Labels;
import io.prometheus.metrics.model.snapshots.Unit;

/**
 * [DEMO] Example Java application instrumented with client_java that serves OpenMetrics 1.0 or 2.0
 * on /metrics, depending on the Accept header. It exposes the same kind of metrics as the Go
 * example (../goapp), so that the OpenMetrics 1.0 and 2.0 scrapes of both can be compared.
 */
public class Main {
  public static void main(String[] args) throws Exception {
    int port = args.length > 0 ? Integer.parseInt(args[0]) : 9400;

    // Enable all OpenMetrics 2.0 features:
    // - enabled: preserves metric names without auto-appending _total or unit suffixes.
    // - contentNegotiation: serves OM 2.0 when Accept has version=2.0.0, OM 1.0 otherwise.
    // - compositeValues: single-line composite histograms and summaries with inline st@.
    // - exemplarCompliance: OM 2.0 compliant exemplars with timestamps.
    // - nativeHistograms: emits native histogram spans and buckets in OM 2.0 text format.
    PrometheusProperties properties =
        PrometheusProperties.builder().enableOpenMetrics2(om2 -> om2.enableAll()).build();

    JvmMetrics.builder().register();

    Info buildInfo =
        Info.builder()
            .name("app_build")
            .help("Application build information.")
            .labelNames("version", "sdk")
            .register();
    buildInfo.setLabelValues("1.0.0", "client_java");

    Counter requests =
        Counter.builder()
            .name("http_requests")
            .help("Total number of HTTP requests handled.")
            .labelNames("method", "status")
            .register();

    Counter requestBytes =
        Counter.builder()
            .name("http_request_size")
            .help("Total HTTP request bytes received.")
            .unit(Unit.BYTES)
            .register();

    // A dotted, OpenTelemetry style name and label, which needs UTF-8 support (quoting) in both
    // OpenMetrics versions.
    Gauge inFlight =
        Gauge.builder()
            .name("http.server.active_requests")
            .help("Current number of in-flight HTTP requests (OTel-style dotted name).")
            .labelNames("service.name")
            .register();

    // A histogram with both classic and native buckets. OpenMetrics 1.0 can only carry the classic
    // buckets, OpenMetrics 2.0 carries both in one composite sample.
    Histogram requestDuration =
        Histogram.builder()
            .name("http_request_duration_seconds")
            .help("HTTP request duration in seconds.")
            .unit(Unit.SECONDS)
            .classicUpperBounds(0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5)
            .labelNames("method")
            .register();

    Summary rpcLatency =
        Summary.builder()
            .name("rpc_latency_seconds")
            .help("RPC latency summary in seconds.")
            .unit(Unit.SECONDS)
            .quantile(0.5, 0.01)
            .quantile(0.9, 0.01)
            .quantile(0.99, 0.001)
            .register();

    Labels exemplar =
        Labels.of("trace_id", "4bf92f3577b34da6a3ce929d0e0e4736", "span_id", "00f067aa0ba902b7");

    // Seed the same initial observations as the Go example.
    requests.labelValues("GET", "200").incWithExemplar(5, exemplar);
    requests.labelValues("POST", "201").inc(2);
    requestBytes.incWithExemplar(4096, exemplar);
    inFlight.labelValues("demo-api").set(3);
    requestDuration.labelValues("GET").observeWithExemplar(0.042, exemplar);
    requestDuration.labelValues("GET").observe(0.18);
    requestDuration.labelValues("GET").observe(0.73);
    rpcLatency.observe(0.015);
    rpcLatency.observe(0.085);

    HTTPServer server =
        HTTPServer.builder(properties).hostname("127.0.0.1").port(port).buildAndStart();
    System.out.println("Serving client_java metrics on http://127.0.0.1:" + server.getPort() + "/metrics");

    while (true) {
      Thread.sleep(2000);
      requests.labelValues("GET", "200").incWithExemplar(1, exemplar);
      requestBytes.incWithExemplar(512, exemplar);
      requestDuration
          .labelValues("GET")
          .observeWithExemplar(0.025 + (Math.random() * 0.2), exemplar);
      rpcLatency.observe(0.01 + (Math.random() * 0.1));
    }
  }
}

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// Change it to e.g. v3.13.0 if you want to install binary.
	// Otherwise, `build` means code from this repo is ran (without UI).
	promVersion = "build"
	port        = 8080
)

// userProjectRoundTripper adds the X-Goog-User-Project header to requests,
// which is required for user Application Default Credentials.
type userProjectRoundTripper struct {
	project string
	next    http.RoundTripper
}

func (rt *userProjectRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.project != "" {
		req.Header.Set("X-Goog-User-Project", rt.project)
	}
	return rt.next.RoundTrip(req)
}

func TestRWtoGCM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	creds, err := google.FindDefaultCredentials(t.Context(), "https://www.googleapis.com/auth/monitoring")
	if err != nil {
		t.Skipf("skipping as Google default credentials are not set (e.g. run 'gcloud auth application-default login'): %v", err)
	}

	projectID := creds.ProjectID
	if projectID == "" {
		for _, envVar := range []string{"GOOGLE_CLOUD_PROJECT", "PROJECT_ID", "CLOUDSDK_CORE_PROJECT"} {
			if p := os.Getenv(envVar); p != "" {
				projectID = p
				break
			}
		}
	}
	if projectID == "" {
		if out, err := exec.CommandContext(t.Context(), "gcloud", "config", "get-value", "project").Output(); err == nil {
			projectID = strings.TrimSpace(string(out))
		}
	}
	if projectID == "" {
		t.Skip("skipping as Google Cloud project ID could not be determined (set GOOGLE_CLOUD_PROJECT or run 'gcloud config set project <project>')")
	}

	var client1, client2, client3 *url.URL
	{
		// OpenMetrics 1 for normal types, including classic histogram on purpose.
		om1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
			w.Write([]byte(`# TYPE test_gauge gauge
test_gauge 1.5
# TYPE test_histogram histogram
test_histogram_bucket{le="1"} 1
test_histogram_bucket{le="+Inf"} 2
test_histogram_sum 2
test_histogram_count 2
# TYPE test_summary summary
test_summary{quantile="0.5"} 1
test_summary_sum 1
test_summary_count 1
# TYPE test_stateset stateset
test_stateset{test_stateset="a"} 1
test_stateset{test_stateset="b"} 0
# TYPE test_info info
test_info_info{foo="bar"} 1
# EOF`))
		}))
		t.Cleanup(om1Server.Close)

		client1, _ = url.Parse(om1Server.URL)
	}
	{
		// Proto for native histogram.
		reg := prometheus.NewRegistry()
		h := prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:                        "test_native_histogram",
			Help:                        "A native histogram.",
			NativeHistogramBucketFactor: 1.1,
		})
		reg.MustRegister(h)
		h.Observe(2.5)
		protoServer := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		t.Cleanup(protoServer.Close)

		client2, _ = url.Parse(protoServer.URL)
	}
	{
		// Another OpenMetrics for classic->NHCB conversion.
		om2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
			w.Write([]byte(`# TYPE test_nhcb_histogram histogram
test_nhcb_histogram_bucket{le="1"} 1
test_nhcb_histogram_bucket{le="+Inf"} 2
test_nhcb_histogram_sum 2
test_nhcb_histogram_count 2
# EOF`))
		}))
		t.Cleanup(om2Server.Close)
		client3, _ = url.Parse(om2Server.URL)
	}

	tmpDir := t.TempDir()

	cluster := "pe-github-action"
	location := "europe-west3-a"
	collector := "prwgcm-test"

	{
		// Start Prometheus.
		config := fmt.Sprintf(`
global:
  scrape_interval: 15s
  scrape_timeout: 15s
  external_labels:
    collector: %s
    project_id: %s
    location: %s
    cluster: %s
scrape_configs:
- job_name: 'om1'
  scrape_interval: 15s
  scrape_timeout: 15s
  static_configs:
  - targets: ['%s']
- job_name: 'proto'
  scrape_interval: 15s
  scrape_timeout: 15s
  scrape_native_histograms: true
  static_configs:
  - targets: ['%s']
- job_name: 'om2'
  scrape_interval: 15s
  scrape_timeout: 15s
  convert_classic_histograms_to_nhcb: true
  static_configs:
  - targets: ['%s']
remote_write:
- name: "google_cloud"
  url: "https://staging-monitoring.sandbox.googleapis.com/v1/prometheus/api/v2/write"
  protobuf_message: "io.prometheus.write.v2.Request"
  # failed_request_logging: true # available on main.
  send_exemplars: true
  send_native_histograms: true
  headers:
    "X-Goog-User-Project": "%s"
  queue_config:
    retry_on_http_429: true
  google_iam: {}
`, collector, projectID, location, cluster, client1.Host, client2.Host, client3.Host, projectID)

		configFile := filepath.Join(tmpDir, "prometheus.yml")
		require.NoError(t, os.WriteFile(configFile, []byte(config), 0600))

		var (
			promBin  string
			promArgs []string
		)
		if promVersion == "build" {
			promBin = promPath
			promArgs = []string{"-test.main"}
		} else {
			var err error
			promBin, err = ensurePrometheusBinary(promVersion)
			require.NoError(t, err)
		}
		promArgs = append(promArgs,
			"--config.file="+configFile,
			fmt.Sprintf("--web.listen-address=0.0.0.0:%d", port),
			"--storage.tsdb.path="+filepath.Join(tmpDir, "data"),
			"--enable-feature=exemplar-storage",
			"--enable-feature=native-histograms",
			"--enable-feature=promql-nhcb-as-classic",
			"--enable-feature=st-storage",
			"--enable-feature=st-synthesis",
			//"--enable-feature=type-and-unit-labels",
			"--enable-feature=metadata-wal-records",
			"--enable-feature=xor2-encoding",
		)
		prom := commandWithLogging(t, nil, promBin, promArgs...)
		require.NoError(t, prom.Start())
		t.Cleanup(func() {
			if prom.Process != nil {
				if err := prom.Process.Kill(); err != nil {
					t.Logf("Failed to kill the process: %v", err)
				}
			}
		})
	}

	require.Eventually(t, func() bool {
		r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/-/ready", port))
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == http.StatusOK
	}, startupTime, 100*time.Millisecond)

	// Queries
	oauthTransport := &oauth2.Transport{
		Source: creds.TokenSource,
		Base:   http.DefaultTransport,
	}
	gcmCl, err := api.NewClient(api.Config{
		Address: fmt.Sprintf("https://staging-monitoring.sandbox.googleapis.com/v1/projects/%s/location/global/prometheus", projectID),
		RoundTripper: &userProjectRoundTripper{
			project: projectID,
			next:    oauthTransport,
		},
	})
	require.NoError(t, err)
	gcmAPI := v1.NewAPI(gcmCl)

	queries := []string{
		`test_gauge{job="om1"}`,
		`test_histogram_count{job="om1"}`,
		`test_summary_count{job="om1"}`,
		`test_stateset{job="om1"}`,
		`test_info_info{job="om1"}`,
		`test_native_histogram_count{job="proto"}`,
		`test_nhcb_histogram_count{job="om2"}`,
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			require.Eventually(t, func() bool {
				gcmVal, _, gcmErr := gcmAPI.Query(t.Context(), query, time.Now())
				if gcmErr != nil {
					return false
				}
				vec, ok := gcmVal.(model.Vector)
				if !ok || len(vec) == 0 {
					return false
				}
				return true
			}, 20*time.Minute, 2*time.Second, "metric %s should be present in GCM", query)
		})
	}
}

// ensurePrometheusBinary ensures that the specified version of the Prometheus binary
// is installed from GitHub releases if not already present, returning the path to the binary.
func ensurePrometheusBinary(version string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("windows is not supported")
	}

	cleanVersion := strings.TrimPrefix(version, "v")
	tag := "v" + cleanVersion

	installDir := filepath.Join(os.TempDir(), "prometheus-"+cleanVersion)
	binPath := filepath.Join(installDir, "prometheus")

	// Check if already installed.
	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", fmt.Errorf("create install dir %s: %w", installDir, err)
	}

	downloadURL := fmt.Sprintf("https://github.com/prometheus/prometheus/releases/download/%s/prometheus-%s.%s-%s.tar.gz", tag, cleanVersion, runtime.GOOS, runtime.GOARCH)

	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download Prometheus from %s: %w", downloadURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Prometheus %s from %s: unexpected status %d", tag, downloadURL, resp.StatusCode)
	}

	tmpBinPath := filepath.Join(installDir, "prometheus.tmp")
	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	var found bool
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tarball: %w", err)
		}

		if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == "prometheus" {
			out, err := os.OpenFile(tmpBinPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
			if err != nil {
				return "", fmt.Errorf("open file %s: %w", tmpBinPath, err)
			}

			if _, err := io.Copy(out, tarReader); err != nil {
				out.Close()
				return "", fmt.Errorf("extract prometheus binary: %w", err)
			}
			if err := out.Close(); err != nil {
				return "", fmt.Errorf("close prometheus binary file: %w", err)
			}
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("prometheus binary not found in tarball %s", downloadURL)
	}

	if err := os.Rename(tmpBinPath, binPath); err != nil {
		return "", fmt.Errorf("rename binary to %s: %w", binPath, err)
	}
	return binPath, nil
}

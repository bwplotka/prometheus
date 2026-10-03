#!/usr/bin/env python3
# Copyright The Prometheus Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""[DEMO] Landing page and demo links of the OpenMetrics 1.0 vs 2.0 scrape demo.

Usage (see deploy.sh):
  landing.py links <Prometheus URL>
  landing.py html <Prometheus version> <client_golang version> <client_java version> <Prometheus flags>
"""

import html
import re
import string
import sys
import urllib.parse

OM2_SPEC = "https://prometheus.io/docs/specs/om/open_metrics_spec_2_0/"
DEMO_CODE = "https://github.com/bwplotka/prometheus/tree/demo-om2-scrape/cmd/prometheus/demo_om2_gce"

# Accept headers that Caddy sets for the /<app>/om1/metrics and
# /<app>/om2/metrics paths, see deploy.sh.
ACCEPT = {
    "om1": "application/openmetrics-text;version=1.0.0;escaping=allow-utf-8",
    "om2": "application/openmetrics-text;version=2.0.0;escaping=allow-utf-8",
}

# The example applications: path prefix, name, SDK and port on the VM.
APPS = [
    ("go", "Go", "client_golang", "8081", "goapp/main.go"),
    ("java", "Java", "client_java", "9400", "javaapp/src/main/java/io/prometheus/examples/Main.java"),
]

# The scrape jobs of prometheus.yml.
JOBS = [
    ("go-om1", "go", "OpenMetricsText1.0.0", "application/openmetrics-text;version=1.0.0;escaping=allow-utf-8"),
    ("go-om2", "go", "OpenMetricsText2.0.0", "application/openmetrics-text;version=2.0.0"),
    ("java-om1", "java", "OpenMetricsText1.0.0", "application/openmetrics-text;version=1.0.0;escaping=allow-utf-8"),
    ("java-om2", "java", "OpenMetricsText2.0.0", "application/openmetrics-text;version=2.0.0"),
]


def only_in(app, a, b):
    """Returns a query for the series of the job <app>-<a> that the job <app>-<b> does not have.

    The metric name is copied to the "name" label, as vector matching ignores __name__.
    """
    sel = lambda v: 'label_replace({job="%s-%s"}, "name", "$1", "__name__", "(.+)")' % (app, v)
    return "group without (job) (%s)\nunless\ngroup without (job) (%s)" % (sel(a), sel(b))


# The demo views in sections, each opening its queries as panels in Prometheus.
SECTIONS = [
    ("Same series?", [
        {
            "id": "series",
            "title": "Series per job",
            "tab": "table",
            "panels": [("All", 'count by (job) ({job=~"(go|java)-om[12]"})')],
            "text": "The number of series each scrape stores. If OpenMetrics 1.0 and 2.0 carried the same data, `go-om1` would match `go-om2` and `java-om1` would match `java-om2`.",
        },
        {
            "id": "go-diff",
            "title": "Go: series stored by only one scrape",
            "tab": "table",
            "panels": [("Only OM1", only_in("go", "om1", "om2")), ("Only OM2", only_in("go", "om2", "om1"))],
            "text": "Both panels are empty when the two scrapes store exactly the same series. For client_golang, OM1 adds `_created` series, which OM2 carries inline as `st@` start timestamps instead. The UTF-8 gauge `http.server.active_requests` is stored as is with OM1, but as `http_server_active_requests` with OM2, as Prometheus does not ask for `escaping=allow-utf-8` when scraping OM2.",
        },
        {
            "id": "java-diff",
            "title": "Java: series stored by only one scrape",
            "tab": "table",
            "panels": [("Only OM1", only_in("java", "om1", "om2")), ("Only OM2", only_in("java", "om2", "om1"))],
            "text": "For client_java with OpenMetrics 2.0 enabled, OM2 exposes counters without the `_total` suffix, e.g. `http_requests` instead of `http_requests_total`, so they are different series. The same UTF-8 escaping difference as for Go applies.",
        },
    ]),
    ("Same values?", [
        {
            "id": "counters",
            "title": "Counters",
            "tab": "graph",
            "panels": [
                ("Requests", 'sum by (job) (rate({__name__=~"http_requests(_total)?", job=~"(go|java)-om[12]"}[1m]))'),
                ("Bytes", 'sum by (job) (rate({__name__=~"http_request_size(_bytes_total)?", job=~"(go|java)-om[12]"}[1m]))'),
            ],
            "text": "The rates of all 4 jobs line up, with a regex on the name to also match the Java OM2 counters without `_total`. They only differ by when each job scrapes.",
        },
        {
            "id": "gauge",
            "title": "UTF-8 gauge",
            "tab": "table",
            "panels": [
                ("UTF-8 name", '{"http.server.active_requests"}'),
                ("Escaped name", "http_server_active_requests"),
            ],
            "text": "The same gauge, with the same value 3, is found under its UTF-8 name for the OM1 jobs and under its escaped name for the OM2 jobs.",
        },
        {
            "id": "histogram",
            "title": "Histograms",
            "tab": "graph",
            "panels": [
                ("p90", 'histogram_quantile(0.9, sum by (job, le) (rate(http_request_duration_seconds_bucket{job=~"(go|java)-om[12]"}[1m])))'),
                ("Rate", 'sum by (job) (rate(http_request_duration_seconds_count{job=~"(go|java)-om[12]"}[1m]))'),
            ],
            "text": "OM2 exposes each histogram as one composite sample, `{count:...,sum:...,bucket:[...]}`, which Prometheus stores as the same classic `_bucket`, `_count` and `_sum` series as the OM1 ones. The native buckets of the composite sample are ignored, as `scrape_native_histograms` is off.",
        },
        {
            "id": "summary",
            "title": "Summaries",
            "tab": "graph",
            "panels": [
                ("p99", 'rpc_latency_seconds{quantile="0.99", job=~"(go|java)-om[12]"}'),
                ("Average", 'sum by (job) (rate(rpc_latency_seconds_sum{job=~"(go|java)-om[12]"}[1m])) / sum by (job) (rate(rpc_latency_seconds_count{job=~"(go|java)-om[12]"}[1m]))'),
            ],
            "text": "Summaries are composite samples in OM2 too, `{count:...,sum:...,quantile:[...]}`, stored as the same `quantile`, `_count` and `_sum` series.",
        },
    ]),
    ("Same exemplars?", [
        {
            "id": "exemplars",
            "title": "Histogram exemplars",
            "tab": "graph",
            "exemplars": True,
            "panels": [
                ("Buckets", 'sum by (job, le) (rate(http_request_duration_seconds_bucket{le=~"0.05|0.1|0.25", job=~"(go|java)-om[12]"}[1m]))'),
                ("Count", 'sum by (job) (rate(http_request_duration_seconds_count{job=~"(go|java)-om[12]"}[1m]))'),
            ],
            "text": "Both opened with exemplars shown. OM1 attaches histogram exemplars to the `_bucket` series of their bucket, while the exemplars of an OM2 composite sample are stored on the `_count` series, so the OM1 jobs show them in the first panel and the OM2 jobs in the second.",
        },
    ]),
]


def query_url(prefix, view):
    params = {}
    for i, (_, expr) in enumerate(view["panels"]):
        params["g%d.expr" % i] = expr
        params["g%d.tab" % i] = view["tab"]
        params["g%d.range_input" % i] = "15m"
        if view.get("exemplars"):
            params["g%d.show_exemplars" % i] = "1"
    return prefix + "query?" + urllib.parse.urlencode(params)


if sys.argv[1] == "links":
    for _, views in SECTIONS:
        for v in views:
            print("    %s: %s" % (v["title"], query_url(sys.argv[2], v)))
    sys.exit()

CSS = """
    :root {
      color-scheme: light dark;
      --brand: #e6522c;
      --accent: hsl(12 76% 44%);
      --accent-hover: hsl(12 78% 38%);
      --accent-text: hsl(12 80% 40%);
      --accent-soft: hsl(12 90% 55% / .1);
      --accent-soft-hover: hsl(12 90% 55% / .18);
      --bg: hsl(36 33% 97%);
      --surface: hsl(0 0% 100%);
      --sunken: hsl(36 20% 95%);
      --text: hsl(222 25% 14%);
      --muted: hsl(222 10% 40%);
      --line: hsl(36 14% 87%);
      --code-bg: hsl(222 30% 98.5%);
      --code-line: hsl(222 20% 90%);
      --mark: hsl(12 95% 55% / .16);
      --shadow: 0 1px 2px hsl(222 30% 20% / .05), 0 10px 30px -14px hsl(222 30% 20% / .16);
      --shadow-hover: 0 2px 4px hsl(222 30% 20% / .06), 0 18px 36px -16px hsl(222 30% 20% / .26);
      --classic-bg: hsl(40 96% 89%);
      --classic-fg: hsl(28 80% 30%);
      --nhcb-bg: hsl(165 50% 87%);
      --nhcb-fg: hsl(172 75% 22%);
      --nhe-bg: hsl(212 90% 92%);
      --nhe-fg: hsl(216 70% 36%);
      --radius: 14px;
      --sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --accent-text: hsl(16 95% 68%);
        --accent-soft: hsl(14 95% 60% / .14);
        --accent-soft-hover: hsl(14 95% 60% / .24);
        --bg: hsl(224 20% 8%);
        --surface: hsl(224 17% 12%);
        --sunken: hsl(224 17% 10%);
        --text: hsl(220 20% 91%);
        --muted: hsl(220 12% 65%);
        --line: hsl(224 13% 21%);
        --code-bg: hsl(224 20% 9%);
        --code-line: hsl(224 13% 19%);
        --mark: hsl(14 95% 60% / .25);
        --shadow: 0 1px 2px hsl(0 0% 0% / .35), 0 10px 30px -14px hsl(0 0% 0% / .7);
        --shadow-hover: 0 2px 4px hsl(0 0% 0% / .4), 0 18px 36px -16px hsl(0 0% 0% / .8);
        --classic-bg: hsl(32 55% 19%);
        --classic-fg: hsl(40 95% 74%);
        --nhcb-bg: hsl(172 45% 15%);
        --nhcb-fg: hsl(164 60% 68%);
        --nhe-bg: hsl(216 50% 20%);
        --nhe-fg: hsl(210 95% 78%);
      }
    }
    *, *::before, *::after { box-sizing: border-box; }
    body {
      margin: 0;
      font: 16px/1.6 var(--sans);
      color: var(--text);
      background: radial-gradient(70rem 32rem at -10% -12rem, hsl(12 90% 55% / .12), transparent 70%) no-repeat, var(--bg);
      -webkit-font-smoothing: antialiased;
    }
    .wrap { max-width: 68rem; margin: 0 auto; padding: 3.5rem 1.25rem 2.5rem; }
    a { color: var(--accent-text); text-decoration-thickness: 1px; text-underline-offset: .2em; }
    a:hover { text-decoration-thickness: 2px; }
    a:focus-visible { outline: 2px solid var(--brand); outline-offset: 2px; border-radius: 6px; }
    code, pre { font-family: var(--mono); }
    code { font-size: .86em; }
    :not(pre) > code { padding: .08em .38em; border: 1px solid var(--line); border-radius: 6px; background: var(--sunken); overflow-wrap: anywhere; }
    h1, h2, h3 { line-height: 1.2; }
    h2 code, h3 code { padding: 0; border: 0; background: none; font-size: .92em; }
    p { margin: 0 0 1rem; }
    .brand { display: flex; align-items: center; gap: .6rem; color: var(--muted); font-size: .95rem; font-weight: 600; }
    h1 { margin: 1.1rem 0 .9rem; font-size: clamp(2.1rem, 1.4rem + 2.8vw, 3.3rem); font-weight: 750; letter-spacing: -.03em; line-height: 1.05; }
    .lead { max-width: 46rem; margin-bottom: 1.75rem; color: var(--muted); font-size: 1.15rem; }
    .actions { display: flex; flex-wrap: wrap; gap: .6rem; }
    .button {
      display: inline-flex; align-items: center; gap: .45rem;
      padding: .6rem 1rem; border: 1px solid var(--line); border-radius: 10px;
      background: var(--surface); color: var(--text); box-shadow: var(--shadow);
      font-size: .95rem; font-weight: 600; line-height: 1.2; text-decoration: none;
      transition: transform .15s ease, box-shadow .15s ease, background-color .15s ease;
    }
    .button:hover { transform: translateY(-1px); box-shadow: var(--shadow-hover); }
    .button small { color: var(--muted); font-size: .85em; font-weight: 500; }
    .button.primary { border-color: transparent; background: var(--accent); color: #fff; }
    .button.primary:hover { background: var(--accent-hover); }
    .button.open { border-color: transparent; background: var(--accent-soft); color: var(--accent-text); box-shadow: none; }
    .button.open:hover { background: var(--accent-soft-hover); }
    .arrow { display: inline-block; transition: transform .15s ease; }
    .button:hover .arrow { transform: translateX(3px); }
    section { margin-top: 4rem; }
    h2 { display: flex; align-items: center; gap: .7rem; margin: 0 0 1.1rem; font-size: 1.55rem; letter-spacing: -.02em; }
    h3 { margin: 0; font-size: 1.12rem; letter-spacing: -.01em; }
    .step { display: inline-grid; place-items: center; flex: none; width: 2rem; height: 2rem; border-radius: 9px; background: var(--accent); color: #fff; font-size: 1rem; font-weight: 700; box-shadow: var(--shadow); }
    .card { border: 1px solid var(--line); border-radius: var(--radius); background: var(--surface); box-shadow: var(--shadow); }
    .table-card { margin-bottom: 1rem; overflow-x: auto; }
    table { width: 100%; border-collapse: collapse; font-size: .95rem; }
    th, td { padding: .75rem 1rem; border-bottom: 1px solid var(--line); text-align: left; vertical-align: top; }
    th { background: var(--sunken); color: var(--muted); font-size: .75rem; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; }
    tbody tr:last-child td { border-bottom: 0; }
    td code { white-space: nowrap; }
    td wbr { display: none; }
    .sub { display: block; margin-top: .25rem; color: var(--muted); font-size: .88rem; }
    .opts { display: flex; flex-direction: column; align-items: flex-start; gap: .3rem; }
    .muted { color: var(--muted); }
    .flags { margin: 0 0 1rem; padding: .8rem 1rem; overflow-x: auto; font-size: .86rem; line-height: 1.6; }
    .chips { display: flex; flex-wrap: wrap; gap: .35rem; }
    .chip { display: inline-flex; align-items: center; gap: .4rem; padding: .28rem .6rem; border-radius: 999px; font: 600 .78rem/1 var(--mono); white-space: nowrap; vertical-align: .05em; }
    .chip::before { content: ""; width: .45rem; height: .45rem; border-radius: 50%; background: currentColor; }
    .chip.classic { background: var(--classic-bg); color: var(--classic-fg); }
    .chip.nhcb { background: var(--nhcb-bg); color: var(--nhcb-fg); }
    .chip.nhe { background: var(--nhe-bg); color: var(--nhe-fg); }
    .legend { display: flex; flex-wrap: wrap; gap: .5rem 1.5rem; margin: 0 0 1rem; padding: 0; color: var(--muted); font-size: .92rem; list-style: none; }
    .legend li { display: flex; align-items: baseline; gap: .5rem; }
    .note { max-width: 52rem; color: var(--muted); font-size: .95rem; }
    .subhead { margin: 2.25rem 0 .8rem; }
    .views { display: grid; gap: 1.1rem; }
    .view { padding: 1.35rem 1.4rem 1.3rem; transition: box-shadow .2s ease; }
    .view:hover { box-shadow: var(--shadow-hover); }
    .view-head { display: flex; align-items: center; justify-content: space-between; gap: .75rem; margin-bottom: .5rem; }
    .view p { max-width: 56rem; }
    .tag { padding: .15rem .55rem; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); font-size: .72rem; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; }
    .queries { display: grid; gap: .5rem; margin-bottom: 1.1rem; }
    .query { display: grid; grid-template-columns: 7.5rem minmax(0, 1fr); overflow: hidden; border: 1px solid var(--code-line); border-radius: 10px; background: var(--code-bg); }
    .query .label { display: flex; flex-direction: column; justify-content: center; padding: .55rem .8rem; border-right: 1px solid var(--code-line); background: var(--sunken); font-size: .82rem; font-weight: 700; }
    .query .label span { color: var(--muted); font-size: .75rem; font-weight: 500; }
    .query pre { margin: 0; padding: .7rem .9rem; font-size: .84rem; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; }
    .query code { font-size: 1em; }
    mark { padding: .05em .2em; border-radius: 4px; background: var(--mark); color: inherit; }
    .controls { margin: 0; padding: .4rem 1.4rem; }
    .controls div { display: grid; grid-template-columns: minmax(14rem, 18rem) 1fr; gap: .4rem 1.5rem; padding: .9rem 0; border-bottom: 1px solid var(--line); }
    .controls div:last-child { border-bottom: 0; }
    .controls dt code { white-space: nowrap; }
    .controls dd { margin: 0; }
    footer { margin-top: 4rem; padding-top: 1.5rem; border-top: 1px solid var(--line); color: var(--muted); font-size: .9rem; }
    footer p { margin: 0 0 .35rem; }
    @media (max-width: 44rem) {
      .wrap { padding-top: 2.25rem; }
      th, td { padding: .65rem .75rem; }
      td code { white-space: normal; overflow-wrap: normal; }
      td wbr { display: inline; }
      .query { grid-template-columns: 1fr; }
      .query .label { flex-direction: row; justify-content: flex-start; align-items: baseline; gap: .5rem; border-right: 0; border-bottom: 1px solid var(--code-line); }
      .controls { padding: .2rem 1.1rem; }
      .controls div { grid-template-columns: 1fr; }
    }
    @media (prefers-reduced-motion: reduce) {
      * { transition: none !important; }
    }
"""

PAGE = string.Template("""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>OpenMetrics 1.0 vs 2.0 scrape demo</title>
  <meta name="description" content="Live demo comparing what Prometheus stores when it scrapes the same Go and Java applications with OpenMetrics 1.0 and OpenMetrics 2.0.">
  <link rel="icon" href="favicon.svg" type="image/svg+xml">
  <style>$css  </style>
</head>
<body>
  <div class="wrap">
    <header>
      <div class="brand"><img src="favicon.svg" alt="" width="30" height="30">Prometheus demo</div>
      <h1>OpenMetrics 1.0 vs 2.0 scrape demo</h1>
      <p class="lead">One Go and one Java application, both instrumented with the Prometheus client library from main, are each scraped twice by Prometheus from main: once with OpenMetrics 1.0 and once with OpenMetrics 2.0. The views below compare what the two scrapes store, series by series.</p>
      <nav class="actions" aria-label="Links">
        <a class="button primary" id="open-prometheus" href="query">Open Prometheus <span class="arrow" aria-hidden="true">&rarr;</span></a>
        <a class="button" href="targets">Targets</a>
        <a class="button" href="$spec">OpenMetrics 2.0 spec</a>
        <a class="button" href="$demo_code">Demo code</a>
      </nav>
    </header>
    <main>
      <section id="setup">
        <h2>Setup</h2>
        <div class="card table-card">
          <table>
            <thead><tr><th>Component</th><th>Version</th></tr></thead>
            <tbody>
              <tr><td>Prometheus</td><td>$prom_version</td></tr>
              <tr><td>client_golang</td><td>$go_version</td></tr>
              <tr><td>client_java</td><td>$java_version</td></tr>
            </tbody>
          </table>
        </div>
        <p>Prometheus $runs and scrapes every 5s with the $njobs jobs below, see its <a href="config">configuration</a>. Each job allows only one protocol in <code>scrape_protocols</code>, so the target has to answer in that format.</p>$flags
        <div class="card table-card">
          <table>
            <thead><tr><th>Job</th><th>Scrape protocol</th><th>Accept header sent</th></tr></thead>
            <tbody>$jobs
            </tbody>
          </table>
        </div>
        <p class="note">Both applications expose the same metrics: a counter with exemplars, a counter with a unit, a gauge with a UTF-8 name and label, a histogram with classic and native buckets, a summary and an info metric, plus the runtime metrics of their SDK.</p>
      </section>
      <section id="exposition">
        <h2>Raw exposition</h2>
        <p>The <code>/metrics</code> endpoints of both applications are public. The <code>om1</code> and <code>om2</code> paths set the Accept header for you, and serve the result as <code>text/plain</code> so that browsers show it; the Accept header sent is in the <code>X-Metrics-Accept</code> response header. The plain path passes your own Accept header through.</p>
        <div class="card table-card">
          <table>
            <thead><tr><th>Application</th><th>OpenMetrics 1.0</th><th>OpenMetrics 2.0</th><th>Negotiated</th></tr></thead>
            <tbody>$apps
            </tbody>
          </table>
        </div>
        <pre class="card flags"><code>$curl</code></pre>
      </section>$sections
    </main>
    <footer>
      <p>Prometheus $prom_version &middot; client_golang $go_version &middot; client_java $java_version</p>
      <p>[DEMO] <a href="$demo_code">Demo code</a>. Not meant to be merged.</p>
    </footer>
  </div>
</body>
</html>""")

SECTION = string.Template("""
      <section id="step-$n">
        <h2><span class="step">$n</span>$title</h2>
        <div class="views">$cards
        </div>
      </section>""")

CARD = string.Template("""
          <article class="card view" id="$id">
            <div class="view-head"><h3>$title</h3><span class="tag">$tab</span></div>
            <p>$text</p>
            <div class="queries">$queries
            </div>
            <a class="button open" id="open-$id" href="$url">Open in Prometheus <span class="arrow" aria-hidden="true">&rarr;</span></a>
          </article>""")

QUERY = string.Template("""
              <div class="query"><div class="label">$label<span>panel $n</span></div><pre><code>$expr</code></pre></div>""")

esc = html.escape


def md(text):
    """Returns text as HTML, with `code` spans."""
    return re.sub(r"`([^`]+)`", r"<code>\1</code>", esc(text, quote=False))


prom_version, go_version, java_version, flags = sys.argv[2:6]

jobs = "".join(
    '\n              <tr><td><code>%s</code><span class="sub"><a href="%s">metadata</a></span></td><td><code>%s</code></td><td><code>%s</code></td></tr>'
    % (job, esc('api/v1/targets/metadata?match_target={job="%s"}' % job), proto, esc(accept))
    for job, _, proto, accept in JOBS)

apps = "".join(
    '\n              <tr><td>%s<span class="sub"><a href="%s">%s</a></span></td><td><a href="%s/om1/metrics">/%s/om1/metrics</a></td><td><a href="%s/om2/metrics">/%s/om2/metrics</a></td><td><a href="%s/metrics">/%s/metrics</a></td></tr>'
    % (name, esc(DEMO_CODE + "/" + src), sdk, p, p, p, p, p, p)
    for p, name, sdk, _, src in APPS)

curl = "\n".join([
    "# OpenMetrics 2.0 from the Go application, as served to Prometheus:",
    "curl -sH '%s' https://&lt;this host&gt;/go/metrics" % ACCEPT["om2"],
    "# The same, through the om2 path that sets the Accept header:",
    "curl -s https://&lt;this host&gt;/go/om2/metrics",
])

sections = []
for n, (title, views) in enumerate(SECTIONS, 1):
    cards = "".join(CARD.substitute(
        id=v["id"], title=md(v["title"]), tab=v["tab"], text=md(v["text"]),
        queries="".join(QUERY.substitute(label=esc(label), n=i, expr=esc(expr)) for i, (label, expr) in enumerate(v["panels"], 1)),
        # Relative links, as the landing page is served on / next to Prometheus.
        url=esc(query_url("", v))) for v in views)
    sections.append(SECTION.substitute(n=n, title=md(title), cards=cards))

print(PAGE.substitute(
    css=CSS,
    spec=esc(OM2_SPEC),
    demo_code=esc(DEMO_CODE),
    prom_version=esc(prom_version),
    go_version=esc(go_version),
    java_version=esc(java_version),
    runs="runs with the flags below" if flags else "runs without demo specific flags",
    flags='\n        <pre class="card flags"><code>%s</code></pre>' % esc("\n".join(flags.split())) if flags else "",
    njobs=len(JOBS),
    jobs=jobs,
    apps=apps,
    curl=curl,
    sections="".join(sections),
))

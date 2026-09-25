#!/usr/bin/env bash
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

# [DEMO] Runs the PromQL histogram compatibility demo (../demo_histcompat_test.go)
# publicly on a single small GCE VM: Prometheus built from GIT_REF with the
# FEATURES flags, --query.convert-histograms-from=CONVERT_FROM and
# ../demo_histcompat.yaml, behind Caddy that serves it without authentication on
# https://<DOMAIN>/ (and http://<IP>/), with a landing page with the demo queries
# on /. The admin and lifecycle APIs stay disabled and queries are limited.
#
# The VM configures itself from its metadata (see startup.sh) and fetches builds
# from a private GCS bucket, so no SSH access is needed. Running "up" again rolls
# out a new build within a minute and keeps the collected data.

set -euo pipefail

usage() {
	cat <<'EOF'
Usage:
  export CLOUDSDK_ACTIVE_CONFIG_NAME=<gcloud configuration> PROJECT=<GCP project>
  deploy.sh up      Build, upload and create or update all resources.
  deploy.sh status  Print the demo URL, its readiness and the demo query links.
  deploy.sh logs    Print the VM startup and rollout logs.
  deploy.sh ssh     SSH into the VM through IAP (extra args go to gcloud compute ssh).
  deploy.sh down    Delete all resources created by "up" (YES=1 skips the prompt).
  deploy.sh build   Only build and stage a release locally, no GCP access needed.

Optional environment variables (defaults in deploy.sh): ZONE, NAME, MACHINE_TYPE,
DOMAIN, FEATURES, CONVERT_FROM, GIT_REF, UI_VERSION, RETENTION.
EOF
}

PROJECT="${PROJECT:-}"
ZONE="${ZONE:-europe-west4-a}"
REGION="${ZONE%-*}"
NAME="${NAME:-prom-histcompat-demo}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-small}"
# Public DNS name pointing to the VM IP, Caddy gets a Let's Encrypt certificate for it.
# Defaults to <IP with dashes>.sslip.io, which resolves to the IP without any setup.
DOMAIN="${DOMAIN:-}"
# Comma separated feature flags to enable.
FEATURES="${FEATURES:-promql-histogram-conversion}"
# Comma separated histogram representations to convert from at query time, see
# --query.convert-histograms-from. Requires the promql-histogram-conversion feature.
CONVERT_FROM="${CONVERT_FROM:-nhcb,nhe,classic}"
# Git revision to build Prometheus and take the demo config from.
GIT_REF="${GIT_REF:-HEAD}"
# Version of the prebuilt web UI release assets to embed. The demo does not change the UI.
UI_VERSION="${UI_VERSION:-3.15.0}"
RETENTION="${RETENTION:-30d}"

# Name of the prom-demo@ systemd service instance on the VM, see startup.sh.
INSTANCE="demo"
# Port Prometheus listens on (on localhost only), as it scrapes itself with
# ../demo_histcompat.yaml.
PORT="1234"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "${SCRIPT_DIR}" rev-parse --show-toplevel)"
# Unauthenticated public endpoints are not allowed in Google's corporate organization.
GOOGLE_ORG_ID="433637338589"
BUILD_DATE=""

log() { echo ">> $*" >&2; }
die() {
	echo "error: $*" >&2
	exit 1
}
gc() { gcloud --project "${PROJECT}" --quiet "$@"; }

# domain_for prints the public DNS name of the IP $1.
domain_for() { echo "${DOMAIN:-${1//./-}.sslip.io}"; }

# prom_flags prints the demo specific Prometheus flags.
prom_flags() {
	local flags="${FEATURES:+--enable-feature=${FEATURES} }${CONVERT_FROM:+--query.convert-histograms-from=${CONVERT_FROM}}"
	echo "${flags% }"
}

check() {
	[[ -n "${PROJECT}" ]] || die "set PROJECT to the GCP project to deploy to."
	BUCKET="${PROJECT}-${NAME}"
	SA_EMAIL="${NAME}@${PROJECT}.iam.gserviceaccount.com"
	log "Using account $(gcloud config get-value account 2>/dev/null) with project ${PROJECT}."
	local org
	org="$(gc projects get-ancestors "${PROJECT}" --format='value(id)' | tail -n1)"
	[[ "${org}" != "${GOOGLE_ORG_ID}" ]] || die "project ${PROJECT} is in the google.com organization, which does not allow unauthenticated public endpoints."
}

# demo_pages prints the demo query links of the Prometheus at the URL $2 if $1
# is "links", or the landing page for the version $2 and the Prometheus flags $3
# if $1 is "html".
demo_pages() {
	python3 - "$@" <<'EOF'
import html
import re
import string
import sys
import urllib.parse

CODE_PR = "https://github.com/bwplotka/prometheus/pull/3"
DEMO_PR = "https://github.com/bwplotka/prometheus/pull/4"
DOCS = "https://github.com/bwplotka/prometheus/blob/histogram-promql/docs/feature_flags.md#query-time-histogram-conversion"

# The scrape jobs of ../demo_histcompat.yaml: name, description, scrape options
# and the representations they store the demo histogram as.
JOBS = [
    ("classic", "Classic histograms, as before a migration.", [], ["classic"]),
    ("nhcb", "Migrated to native histograms with custom buckets, converted on scrape.",
     ["convert_classic_histograms_to_nhcb: true"], ["nhcb"]),
    ("native", "Migrated to native histograms with exponential buckets.",
     ["scrape_native_histograms: true", "convert_classic_histograms_to_nhcb: true"], ["nhe"]),
    ("classic-and-native", "In the middle of a migration, scraping both.",
     ["scrape_native_histograms: true", "always_scrape_classic_histograms: true"], ["classic", "nhe"]),
]
REPRESENTATIONS = [
    ("classic", "classic histogram series: `_bucket`, `_count` and `_sum`"),
    ("nhcb", "native histogram with custom buckets (NHCB)"),
    ("nhe", "native histogram with exponential buckets"),
]

# The demo views in sections, each opening a classic histogram query (top
# panel) next to its native histogram equivalent (bottom panel). Keep in sync
# with openDemoTabs in ../demo_histcompat_test.go.
SECTIONS = [
    ("What is stored", [
        {
            "id": "stored",
            "title": "Stored histograms",
            "tab": "table",
            "classic": 'sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds_count{__convert_stored_as__="", __debug_stored_as__="true"}[5m]))',
            "native": 'sum by (job, __stored_as__) (prometheus_http_request_duration_seconds{__convert_stored_as__="", __debug_stored_as__="true"})',
            "text": '`__convert_stored_as__=""` turns the conversion off and `__debug_stored_as__="true"` adds the `__stored_as__` label, so both queries return what is stored, as without the feature: the classic query only finds `classic` and `classic-and-native`, the native query only the native histograms of `nhcb`, `native` and `classic-and-native`, drawn as bucket charts.',
        },
    ]),
    ("Counts and quantiles work in both forms", [
        {
            "id": "rates",
            "title": "Request rates",
            "tab": "table",
            "classic": 'sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds_count{__debug_stored_as__="true"}[5m]))',
            "native": 'histogram_count(sum by (job, __stored_as__) (rate(prometheus_http_request_duration_seconds{__debug_stored_as__="true"}[5m])))',
            "text": "With the conversion on, both forms return all jobs with the same rates, and `__stored_as__` shows the representation each result is read from. `classic-and-native` is not counted twice: stored data wins, so each query reads the histogram stored in its own form.",
        },
        {
            "id": "quantiles",
            "title": "99th percentile",
            "tab": "graph",
            "classic": "histogram_quantile(0.99, sum by (job, le) (rate(prometheus_http_request_duration_seconds_bucket[5m])))",
            "native": "histogram_quantile(0.99, sum by (job) (rate(prometheus_http_request_duration_seconds[5m])))",
            "text": "Quantiles work for all jobs in both forms. They differ with the stored buckets, not with the query form: the exponential buckets of `native`, and of `classic-and-native` in the native query, are much finer than the classic ones, so they give a more precise p99.",
        },
    ]),
    ("Buckets and `le`", [
        {
            "id": "buckets",
            "title": "Bucket series",
            "tab": "table",
            "classic": "sum by (job, le) (rate(prometheus_http_request_duration_seconds_bucket[5m]))",
            "native": "sum by (job) (rate(prometheus_http_request_duration_seconds[5m]))",
            "text": "Classic `_bucket` queries return `le` series for all jobs, and native queries a histogram per job, drawn as a bucket chart. NHCB keep their bucket boundaries. Exponential buckets have no fixed boundaries, so their `le` values are derived from the populated buckets of all selected histograms in the queried range: powers of 2^{1/8} like 0.0964... or 0.1051..., but never 0.1.",
        },
        {
            "id": "le-filter",
            "title": "Filtering by `le`",
            "tab": "table",
            "classic": 'sum by (job) (rate(prometheus_http_request_duration_seconds_bucket{le="0.1"}[5m])) / sum by (job) (rate(prometheus_http_request_duration_seconds_count[5m]))',
            "native": "histogram_fraction(0, 0.1, sum by (job) (rate(prometheus_http_request_duration_seconds[5m])))",
            "text": 'The share of requests faster than 100ms, a common SLO query. The classic form misses the `native` job, as `le="0.1"` matches none of its exponential boundaries, while `classic-and-native` reads its stored classic histogram. `histogram_fraction` works for all jobs, interpolating within the exponential bucket around 0.1.',
        },
    ]),
]
CONVERT_EXAMPLE = 'sum by (job) (rate(prometheus_http_request_duration_seconds_count{__convert_stored_as__="nhe"}[5m]))'


def query_url(prefix, tab, exprs):
    params = {}
    for i, expr in enumerate(exprs):
        params["g%d.expr" % i] = expr
        params["g%d.tab" % i] = tab
    return prefix + "query?" + urllib.parse.urlencode(params)


if sys.argv[1] == "links":
    for _, views in SECTIONS:
        for v in views:
            url = query_url(sys.argv[2], v["tab"], (v["classic"], v["native"]))
            print("    %s: %s" % (v["title"].replace("`", ""), url))
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
  <title>PromQL histogram conversion demo</title>
  <meta name="description" content="Live demo of a Prometheus prototype that converts between classic and native histograms at query time, so that queries for either keep working.">
  <link rel="icon" href="favicon.svg" type="image/svg+xml">
  <style>$css  </style>
</head>
<body>
  <div class="wrap">
    <header>
      <div class="brand"><img src="favicon.svg" alt="" width="30" height="30">Prometheus prototype</div>
      <h1>PromQL histogram conversion demo</h1>
      <p class="lead">Queries written for classic histograms keep working on native histograms, and the other way around, as this Prometheus converts between them at query time. Each view below opens a classic histogram query next to its native histogram equivalent.</p>
      <nav class="actions" aria-label="Links">
        <a class="button primary" id="open-prometheus" href="query">Open Prometheus <span class="arrow" aria-hidden="true">&rarr;</span></a>
        <a class="button" id="code-pr" href="$code_pr">Code <small>PR #3</small></a>
        <a class="button" id="demo-pr" href="$demo_pr">Demo code <small>PR #4</small></a>
        <a class="button" id="docs" href="$docs">Documentation</a>
      </nav>
    </header>
    <main>
      <section id="setup">
        <h2>Setup</h2>
        <p>This Prometheus $runs and scrapes itself every 5s with $njobs jobs, see its <a href="config">configuration</a> and <a href="targets">targets</a>. It exposes <code>prometheus_http_request_duration_seconds</code> with both classic buckets, from 0.1s to 120s, and exponential native buckets, and each job stores it in a different representation.</p>$flags
        <div class="card table-card">
          <table>
            <thead><tr><th>Job</th><th>Scrape options</th><th>Stored as</th></tr></thead>
            <tbody>$jobs
            </tbody>
          </table>
        </div>
        <ul class="legend">$legend
        </ul>
        <p class="note">The <code>native</code> job also converts histograms exposed with classic buckets only, like <code>prometheus_http_response_size_bytes</code>, to NHCB, so it stores no classic histograms at all.</p>
        <h3 class="subhead">How queries read histograms</h3>
        <div class="card table-card">
          <table>
            <thead><tr><th>Selector</th><th>Returns</th></tr></thead>
            <tbody>
              <tr><td>$classic_selectors<span class="sub">Classic histogram queries</span></td><td>$returns_classic</td></tr>
              <tr><td>$native_selector<span class="sub">Native histogram queries</span></td><td>$returns_native</td></tr>
            </tbody>
          </table>
        </div>
        <p class="note">Where a histogram is both stored and converted, stored data wins, so nothing is counted twice. Only selectors with a metric name equality matcher are converted, e.g. <code>{__name__=~"prometheus_http_request_duration_seconds.*"}</code> returns stored series only.</p>
      </section>
      <section id="controls">
        <h2>Control matchers</h2>
        <dl class="card controls">
          <div><dt><code>__debug_stored_as__="true"</code></dt><dd>Adds a <code>__stored_as__</code> label holding the representation the samples of each result are stored as.</dd></div>
          <div><dt><code>__convert_stored_as__=""</code></dt><dd>Turns the conversion off for the selector, which then returns the stored series as if the feature were disabled.</dd></div>
          <div><dt><code>__convert_stored_as__="nhe"</code></dt><dd>Reads only the matching representations, stored or converted, e.g. <a id="convert-example" href="$convert_url">only the rates converted from exponential histograms</a>.</dd></div>
        </dl>
      </section>$sections
    </main>
    <footer>
      <p>Prometheus $version &middot; <a href="$code_pr">Code</a> &middot; <a href="$demo_pr">Demo code</a> &middot; <a href="$docs">Documentation</a></p>
      <p>[DEMO] Not meant to be merged.</p>
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
            <div class="queries">
              <div class="query"><div class="label">Classic<span>top panel</span></div><pre><code>$classic</code></pre></div>
              <div class="query"><div class="label">Native<span>bottom panel</span></div><pre><code>$native</code></pre></div>
            </div>
            <a class="button open" id="open-$id" href="$url">Open in Prometheus <span class="arrow" aria-hidden="true">&rarr;</span></a>
          </article>""")

# The matchers the views are about, highlighted in their queries.
MARKS = re.compile(r'(__(?:convert|debug)_stored_as__="[^"]*"|le="0\.1")')

esc = html.escape


def md(text):
    """Returns text as HTML, with `code` spans, ^{superscripts} and ellipses."""
    out = esc(text, quote=False).replace("...", "&hellip;")
    out = re.sub(r"`([^`]+)`", r"<code>\1</code>", out)
    return re.sub(r"\^\{([^}]+)\}", r"<sup>\1</sup>", out)


def query_html(expr):
    return "".join("<mark>%s</mark>" % esc(p) if i % 2 else esc(p) for i, p in enumerate(MARKS.split(expr)))


def chips(reps):
    return " ".join('<span class="chip %s">%s</span>' % (r, r) for r in reps)


def table_code(text):
    """Returns text as code that can wrap after underscores on narrow screens."""
    return "<code>%s</code>" % esc(text).replace("_", "_<wbr>")


version, flags = sys.argv[2], sys.argv[3]
m = re.search(r"--query\.convert-histograms-from=(\S+)", flags)
convert_from = m.group(1).split(",") if m else []


def returns(stored, converted):
    converted = [r for r in converted if r in convert_from]
    return "Stored %s%s" % (chips(stored), ", plus converted from " + chips(converted) if converted else "")


jobs = []
for name, desc, opts, reps in JOBS:
    options = '<span class="muted">Defaults</span>'
    if opts:
        options = '<div class="opts">%s</div>' % "".join(table_code(o) for o in opts)
    jobs.append('\n              <tr><td>%s<span class="sub">%s</span></td><td>%s</td><td><div class="chips">%s</div></td></tr>'
                % (table_code(name), md(desc), options, chips(reps)))

sections = []
for n, (title, views) in enumerate(SECTIONS, 1):
    cards = "".join(CARD.substitute(
        id=v["id"], title=md(v["title"]), tab=v["tab"], text=md(v["text"]),
        classic=query_html(v["classic"]), native=query_html(v["native"]),
        # Relative links, as the landing page is served on / next to Prometheus.
        url=esc(query_url("", v["tab"], (v["classic"], v["native"])))) for v in views)
    sections.append(SECTION.substitute(n=n, title=md(title), cards=cards))

print(PAGE.substitute(
    css=CSS,
    code_pr=esc(CODE_PR),
    demo_pr=esc(DEMO_PR),
    docs=esc(DOCS),
    runs="runs with the flags below" if flags else "runs without demo specific flags",
    flags='\n        <pre class="card flags"><code>%s</code></pre>' % esc("\n".join(flags.split())) if flags else "",
    njobs=len(JOBS),
    jobs="".join(jobs),
    legend="".join("\n          <li>%s <span>%s</span></li>" % (chips([r]), md(d)) for r, d in REPRESENTATIONS),
    classic_selectors=", ".join(table_code(s) for s in ("prometheus_http_request_duration_seconds_bucket", "_count", "_sum")),
    native_selector=table_code("prometheus_http_request_duration_seconds"),
    returns_classic=returns(["classic"], ["nhcb", "nhe"]),
    returns_native=returns(["nhcb", "nhe"], ["classic"]),
    sections="".join(sections),
    convert_url=esc(query_url("", "table", (CONVERT_EXAMPLE,))),
    version=esc(version),
))
EOF
}

# build stages a release for the external IP $1 into the directory $2: the
# Prometheus binary, its config and systemd environment file, the Caddyfile and
# the landing page.
build() {
	local ip="$1" stage="$2" domain src ui cache ui_tgz rev branch version flags
	local pkg="github.com/prometheus/common/version"
	domain="$(domain_for "${ip}")"
	src="$(mktemp -d)"
	ui="$(mktemp -d)"
	rev="$(git -C "${REPO_ROOT}" rev-parse --short=12 "${GIT_REF}")"
	branch="$(git -C "${REPO_ROOT}" rev-parse --abbrev-ref "${GIT_REF}" 2>/dev/null || echo "${GIT_REF}")"
	BUILD_DATE="$(date -u +%Y%m%d-%H:%M:%S)"

	log "Building Prometheus ${rev} (${GIT_REF}) with the v${UI_VERSION} web UI."
	git -C "${REPO_ROOT}" archive --format=tar "${GIT_REF}" | tar -x -C "${src}"
	cache="${XDG_CACHE_HOME:-${HOME}/.cache}/${NAME}"
	ui_tgz="${cache}/prometheus-web-ui-${UI_VERSION}.tar.gz"
	if [[ ! -f "${ui_tgz}" ]]; then
		mkdir -p "${cache}"
		curl -fsSL -o "${ui_tgz}.tmp" "https://github.com/prometheus/prometheus/releases/download/v${UI_VERSION}/prometheus-web-ui-${UI_VERSION}.tar.gz"
		mv "${ui_tgz}.tmp" "${ui_tgz}"
	fi
	tar -xzf "${ui_tgz}" -C "${ui}"
	(cd "${src}" && PREBUILT_ASSETS_STATIC_DIR="${ui}/static" scripts/compress_assets.sh)

	version="$(cat "${src}/VERSION")-histcompat-demo"
	mkdir -p "${stage}/configs" "${stage}/env" "${stage}/www"
	(
		cd "${src}"
		export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
		go build -trimpath -tags netgo,builtinassets -o "${stage}/prometheus" \
			-ldflags "-s -w -X ${pkg}.Version=${version} -X ${pkg}.Revision=${rev} -X ${pkg}.Branch=${branch} -X ${pkg}.BuildUser=${USER} -X ${pkg}.BuildDate=${BUILD_DATE}" \
			./cmd/prometheus
		go build -o "${src}/promtool" ./cmd/promtool
	)

	{
		cat "${src}/cmd/prometheus/demo_histcompat.yaml"
		printf '\nstorage:\n  tsdb:\n    retention:\n      time: %s\n      size: 2GB\n' "${RETENTION}"
	} >"${stage}/configs/${INSTANCE}.yml"
	"${src}/promtool" check config --syntax-only "${stage}/configs/${INSTANCE}.yml" >&2
	grep -q "localhost:${PORT}" "${stage}/configs/${INSTANCE}.yml" || die "demo_histcompat.yaml does not scrape localhost:${PORT}."
	flags="$(prom_flags)"
	{
		echo "PORT=${PORT}"
		echo "TITLE=\"PromQL histogram conversion demo\""
		echo "EXTRA_ARGS=\"--web.external-url=https://${domain}/${flags:+ ${flags}}\""
	} >"${stage}/env/${INSTANCE}.env"
	echo "${INSTANCE}" >"${stage}/instances"

	# Caddy serves the landing page on / only, and everything else from
	# Prometheus. The UI redirects / to /query on the client side.
	{
		echo "# [DEMO] Generated by deploy.sh."
		echo "(demo) {"
		printf '\thandle / {\n\t\troot * /opt/prom-demo/current/www\n\t\tfile_server\n\t}\n'
		printf '\thandle {\n\t\treverse_proxy 127.0.0.1:%s\n\t}\n}\n\n' "${PORT}"
		printf 'http://%s {\n\timport demo\n}\n\n%s {\n\timport demo\n}\n' "${ip}" "${domain}"
	} >"${stage}/Caddyfile"
	demo_pages html "${version} (${rev})" "${flags}" >"${stage}/www/index.html"
	rm -rf "${src}" "${ui}"
}

# wait_ready waits until Prometheus on the IP $1 serves the build from BUILD_DATE.
wait_ready() {
	local ip="$1" deadline=$((SECONDS + 600))
	log "Waiting for the VM to serve the new build, see \"deploy.sh logs\" for progress."
	until curl -fsS -m 5 "http://${ip}/api/v1/status/buildinfo" 2>/dev/null | grep -q "\"buildDate\":\"${BUILD_DATE}\""; do
		if ((SECONDS > deadline)); then
			log "Timed out waiting for http://${ip}/, check \"deploy.sh logs\"."
			return 0
		fi
		sleep 5
	done
	log "Prometheus is serving the new build."
}

# ensure_firewall allows the ingress $2 from the source ranges $3 to the VM, with
# the description $4, in the firewall rule $1.
ensure_firewall() {
	gc compute firewall-rules describe "$1" >/dev/null 2>&1 && return
	log "Allowing $2 from $3 to the VM."
	gc compute firewall-rules create "$1" --network "${NAME}" --direction INGRESS --action ALLOW \
		--rules "$2" --source-ranges "$3" --target-tags "${NAME}" --description "$4"
}

up() {
	check
	log "Enabling the required APIs."
	gc services enable compute.googleapis.com storage.googleapis.com iam.googleapis.com

	if ! gc compute addresses describe "${NAME}" --region "${REGION}" >/dev/null 2>&1; then
		log "Reserving a static external IP."
		gc compute addresses create "${NAME}" --region "${REGION}"
	fi
	local ip stage release
	ip="$(gc compute addresses describe "${NAME}" --region "${REGION}" --format='value(address)')"

	stage="$(mktemp -d)"
	build "${ip}" "${stage}"
	release="$(date -u +%Y%m%dT%H%M%SZ)-$(git -C "${REPO_ROOT}" rev-parse --short=12 "${GIT_REF}")"
	tar -czf "${stage}.tgz" -C "${stage}" .

	if ! gc storage buckets describe "gs://${BUCKET}" >/dev/null 2>&1; then
		log "Creating the private release bucket gs://${BUCKET}."
		gc storage buckets create "gs://${BUCKET}" --location "${REGION}" --uniform-bucket-level-access --public-access-prevention
	fi
	if ! gc iam service-accounts describe "${SA_EMAIL}" >/dev/null 2>&1; then
		log "Creating the VM service account ${SA_EMAIL}."
		gc iam service-accounts create "${NAME}" --display-name "${NAME} VM, can only read gs://${BUCKET}"
	fi
	# A new service account can take a few seconds to be usable in IAM policies.
	local i out
	for i in 1 2 3 4 5 6; do
		out="$(gc storage buckets add-iam-policy-binding "gs://${BUCKET}" --member "serviceAccount:${SA_EMAIL}" --role roles/storage.objectViewer 2>&1)" && break
		((i < 6)) || die "cannot grant ${SA_EMAIL} read access to gs://${BUCKET}: ${out}"
		sleep 10
	done
	log "Uploading release ${release}."
	gc storage cp "${stage}.tgz" "gs://${BUCKET}/releases/${release}.tgz"
	rm -rf "${stage}" "${stage}.tgz"

	# A dedicated network, so that only the rules below apply to the VM.
	if ! gc compute networks describe "${NAME}" >/dev/null 2>&1; then
		log "Creating the VPC network ${NAME}."
		gc compute networks create "${NAME}" --subnet-mode custom
	fi
	if ! gc compute networks subnets describe "${NAME}" --region "${REGION}" >/dev/null 2>&1; then
		gc compute networks subnets create "${NAME}" --network "${NAME}" --region "${REGION}" --range 10.0.0.0/24
	fi
	ensure_firewall "${NAME}-web" tcp:80,tcp:443,udp:443 0.0.0.0/0 "Public, unauthenticated access to the Prometheus histogram compat demo."
	ensure_firewall "${NAME}-iap-ssh" tcp:22 35.235.240.0/20 "SSH through IAP for deploy.sh ssh."

	local metadata="bucket=${BUCKET},release=${release}"
	if gc compute instances describe "${NAME}" --zone "${ZONE}" >/dev/null 2>&1; then
		log "Rolling out ${release} to the VM ${NAME}."
		gc compute instances add-metadata "${NAME}" --zone "${ZONE}" \
			--metadata "${metadata}" --metadata-from-file "startup-script=${SCRIPT_DIR}/startup.sh"
	else
		log "Creating the VM ${NAME}."
		gc compute instances create "${NAME}" --zone "${ZONE}" --machine-type "${MACHINE_TYPE}" \
			--subnet "${NAME}" --address "${ip}" --tags "${NAME}" \
			--image-family debian-13 --image-project debian-cloud --boot-disk-size 20GB --boot-disk-type pd-balanced \
			--service-account "${SA_EMAIL}" --scopes storage-ro \
			--shielded-secure-boot --shielded-vtpm --shielded-integrity-monitoring \
			--labels "app=${NAME}" \
			--metadata "${metadata}" --metadata-from-file "startup-script=${SCRIPT_DIR}/startup.sh"
	fi
	wait_ready "${ip}"
	status
}

status() {
	[[ -n "${PROJECT}" ]] || die "set PROJECT to the GCP project to deploy to."
	local ip domain ready flags
	ip="$(gc compute addresses describe "${NAME}" --region "${REGION}" --format='value(address)' 2>/dev/null)" || die "no ${NAME} deployment in ${PROJECT}."
	domain="$(domain_for "${ip}")"
	ready="$(curl -fsS -m 10 "https://${domain}/-/ready" 2>&1 || true)"
	flags="$(prom_flags)"
	echo "Landing page: https://${domain}/ (or plain HTTP http://${ip}/)"
	echo "Prometheus${flags:+ (${flags})}: https://${domain}/query [${ready}]"
	demo_pages links "https://${domain}/"
}

logs() {
	[[ -n "${PROJECT}" ]] || die "set PROJECT to the GCP project to deploy to."
	gc compute instances get-serial-port-output "${NAME}" --zone "${ZONE}" 2>/dev/null |
		grep -E 'startup-script|prom-demo' | sed -E 's/^.*command\("\/bin\/bash"\): //' |
		tail -n "${LOG_LINES:-60}" || log "No startup logs yet."
}

down() {
	check
	if [[ "${YES:-}" != 1 ]]; then
		local answer
		read -r -p "Delete the VM, IP, network, bucket and service account ${NAME} in ${PROJECT}? [y/N] " answer
		[[ "${answer}" == y ]] || exit 1
	fi
	gc compute instances delete "${NAME}" --zone "${ZONE}" || true
	gc compute firewall-rules delete "${NAME}-web" || true
	gc compute firewall-rules delete "${NAME}-iap-ssh" || true
	gc compute networks subnets delete "${NAME}" --region "${REGION}" || true
	gc compute networks delete "${NAME}" || true
	gc compute addresses delete "${NAME}" --region "${REGION}" || true
	gc storage rm --recursive "gs://${BUCKET}" || true
	gc iam service-accounts delete "${SA_EMAIL}" || true
}

case "${1:-}" in
up) up ;;
status) status ;;
logs) logs ;;
ssh)
	shift
	[[ -n "${PROJECT}" ]] || die "set PROJECT to the GCP project to deploy to."
	gc compute ssh "${NAME}" --zone "${ZONE}" --tunnel-through-iap "$@"
	;;
down) down ;;
build)
	stage="${2:-$(mktemp -d)}"
	build "127.0.0.1" "${stage}"
	log "Staged the release in ${stage}."
	;;
*)
	usage
	exit 1
	;;
esac

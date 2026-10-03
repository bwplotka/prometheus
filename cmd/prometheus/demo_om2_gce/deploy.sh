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

# [DEMO] Runs the OpenMetrics 1.0 vs 2.0 scrape demo publicly on a single small
# GCE VM: Prometheus built from GIT_REF (main plus this demo) with the FEATURES
# flags and prometheus.yml, scraping a Go example built with client_golang
# GO_CLIENT_REF and a Java example built with client_java JAVA_CLIENT_REF, each
# with OpenMetrics 1.0 and 2.0. Caddy serves Prometheus without authentication
# on https://<DOMAIN>/ (and http://<IP>/), with a landing page with the demo
# queries on /, and the /metrics endpoints of both examples on
# /<go|java>/metrics, /<go|java>/om1/metrics and /<go|java>/om2/metrics. The
# admin and lifecycle APIs stay disabled and queries are limited.
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
DOMAIN, FEATURES, GIT_REF, GO_CLIENT_REF, JAVA_CLIENT_REF, UI_VERSION, RETENTION.
EOF
}

PROJECT="${PROJECT:-}"
ZONE="${ZONE:-europe-west4-a}"
REGION="${ZONE%-*}"
NAME="${NAME:-prom-om2-demo}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-small}"
# Public DNS name pointing to the VM IP, Caddy gets a Let's Encrypt certificate for it.
# Defaults to <IP with dashes>.sslip.io, which resolves to the IP without any setup.
DOMAIN="${DOMAIN:-}"
# Comma separated feature flags to enable.
FEATURES="${FEATURES:-openmetrics2,exemplar-storage}"
# Git revision to build Prometheus and take the demo files from.
GIT_REF="${GIT_REF:-HEAD}"
# Revisions of client_golang and client_java to build the examples with.
GO_CLIENT_REF="${GO_CLIENT_REF:-main}"
JAVA_CLIENT_REF="${JAVA_CLIENT_REF:-main}"
# Version of the prebuilt web UI release assets to embed. The demo does not change the UI.
UI_VERSION="${UI_VERSION:-3.15.0}"
RETENTION="${RETENTION:-30d}"

# Name of the prom-demo@ systemd service instance on the VM, see startup.sh.
INSTANCE="demo"
# Ports Prometheus and the examples listen on, on localhost only. They have to
# match prometheus.yml.
PORT="1234"
GO_PORT="8081"
JAVA_PORT="9400"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "${SCRIPT_DIR}" rev-parse --show-toplevel)"
# The demo directory relative to the repository root, to find it in GIT_REF.
DEMO_DIR="$(git -C "${SCRIPT_DIR}" rev-parse --show-prefix)"
DEMO_DIR="${DEMO_DIR%/}"
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
prom_flags() { echo "${FEATURES:+--enable-feature=${FEATURES}}"; }

check() {
	[[ -n "${PROJECT}" ]] || die "set PROJECT to the GCP project to deploy to."
	BUCKET="${PROJECT}-${NAME}"
	SA_EMAIL="${NAME}@${PROJECT}.iam.gserviceaccount.com"
	log "Using account $(gcloud config get-value account 2>/dev/null) with project ${PROJECT}."
	local org
	org="$(gc projects get-ancestors "${PROJECT}" --format='value(id)' | tail -n1)"
	[[ "${org}" != "${GOOGLE_ORG_ID}" ]] || die "project ${PROJECT} is in the google.com organization, which does not allow unauthenticated public endpoints."
}

# remote_sha prints the commit of the branch, tag or commit $2 of the GitHub repository $1.
remote_sha() {
	local sha
	sha="$(git ls-remote "https://github.com/$1.git" "$2" | head -n1 | cut -f1)"
	echo "${sha:-$2}"
}

# caddy_metrics prints the Caddyfile routes exposing /metrics of the example
# on the port $2 under /$1/. The om1 and om2 paths set the Accept header, and
# rewrite the response content type to text/plain so that browsers show it.
# The Accept header sent is returned in X-Metrics-Accept.
# NOTE: The {http.reverse_proxy.header.*} placeholders do not work in
# header_down with the Caddy 2.6 of Debian 13, so the negotiated content type
# cannot be returned.
caddy_metrics() {
	local app="$1" port="$2" v
	printf '\thandle /%s/metrics {\n\t\trewrite * /metrics\n\t\treverse_proxy 127.0.0.1:%s\n\t}\n' "${app}" "${port}"
	for v in 1 2; do
		printf '\thandle /%s/om%s/metrics {\n\t\trewrite * /metrics\n\t\treverse_proxy 127.0.0.1:%s {\n' "${app}" "${v}" "${port}"
		printf '\t\t\theader_up Accept "application/openmetrics-text;version=%s.0.0;escaping=allow-utf-8"\n' "${v}"
		printf '\t\t\theader_down X-Metrics-Accept "application/openmetrics-text;version=%s.0.0;escaping=allow-utf-8"\n' "${v}"
		printf '\t\t\theader_down Content-Type "text/plain; charset=utf-8"\n'
		printf '\t\t}\n\t}\n'
	done
}

# build stages a release for the external IP $1 into the directory $2: the
# Prometheus binary, its config and systemd environment file, the examples,
# the Caddyfile and the landing page.
build() {
	local ip="$1" stage="$2" domain src ui cache ui_tgz rev branch version flags
	local go_ref java_ref go_version java_version
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

	version="$(cat "${src}/VERSION")-om2-demo"
	mkdir -p "${stage}/configs" "${stage}/env" "${stage}/www" "${stage}/apps/go" "${stage}/apps/java"
	(
		cd "${src}"
		export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
		go build -trimpath -tags netgo,builtinassets -o "${stage}/prometheus" \
			-ldflags "-s -w -X ${pkg}.Version=${version} -X ${pkg}.Revision=${rev} -X ${pkg}.Branch=${branch} -X ${pkg}.BuildUser=${USER} -X ${pkg}.BuildDate=${BUILD_DATE}" \
			./cmd/prometheus
		GOOS= GOARCH= go build -o "${src}/promtool" ./cmd/promtool
	)

	# The Go example is its own module, outside of the Prometheus go.work.
	go_ref="$(remote_sha prometheus/client_golang "${GO_CLIENT_REF}")"
	log "Building the Go example with client_golang ${go_ref}."
	(
		cd "${src}/${DEMO_DIR}/goapp"
		export GOWORK=off GOFLAGS=-mod=mod
		go get "github.com/prometheus/client_golang@${go_ref}" >&2
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "${stage}/apps/go/goapp" .
	)
	go_version="$(cd "${src}/${DEMO_DIR}/goapp" && GOWORK=off go list -m -f '{{.Version}}' github.com/prometheus/client_golang)"
	printf '#!/bin/sh\nexec ./goapp --listen-address=127.0.0.1:%s\n' "${GO_PORT}" >"${stage}/apps/go/run"

	java_ref="$(remote_sha prometheus/client_java "${JAVA_CLIENT_REF}")"
	log "Building client_java ${java_ref} and the Java example with Docker."
	docker build -q --build-arg "CLIENT_JAVA_REF=${java_ref}" --output "type=local,dest=${stage}/apps/java" "${src}/${DEMO_DIR}/javaapp" >&2
	java_version="$(cat "${stage}/apps/java/client_java.version")"
	# Small heap, as it shares the e2-small VM with Prometheus.
	printf '#!/bin/sh\nexec java -Xmx96m -XX:+UseSerialGC -jar app.jar %s\n' "${JAVA_PORT}" >"${stage}/apps/java/run"
	chmod 0755 "${stage}/apps/go/run" "${stage}/apps/java/run"
	printf 'go\njava\n' >"${stage}/app-list"

	{
		cat "${src}/${DEMO_DIR}/prometheus.yml"
		printf '\nstorage:\n  tsdb:\n    retention:\n      time: %s\n      size: 2GB\n' "${RETENTION}"
	} >"${stage}/configs/${INSTANCE}.yml"
	"${src}/promtool" check config --syntax-only "${stage}/configs/${INSTANCE}.yml" >&2
	grep -q "localhost:${GO_PORT}" "${stage}/configs/${INSTANCE}.yml" || die "prometheus.yml does not scrape localhost:${GO_PORT}."
	grep -q "localhost:${JAVA_PORT}" "${stage}/configs/${INSTANCE}.yml" || die "prometheus.yml does not scrape localhost:${JAVA_PORT}."
	flags="$(prom_flags)"
	{
		echo "PORT=${PORT}"
		echo "TITLE=\"OpenMetrics 1.0 vs 2.0 scrape demo\""
		echo "EXTRA_ARGS=\"--web.external-url=https://${domain}/${flags:+ ${flags}}\""
	} >"${stage}/env/${INSTANCE}.env"
	echo "${INSTANCE}" >"${stage}/instances"

	# Caddy serves the landing page on / only, the example /metrics endpoints,
	# and everything else from Prometheus. The UI redirects / to /query on the
	# client side.
	{
		echo "# [DEMO] Generated by deploy.sh."
		echo "(demo) {"
		printf '\thandle / {\n\t\troot * /opt/prom-demo/current/www\n\t\tfile_server\n\t}\n'
		caddy_metrics go "${GO_PORT}"
		caddy_metrics java "${JAVA_PORT}"
		printf '\thandle {\n\t\treverse_proxy 127.0.0.1:%s\n\t}\n}\n\n' "${PORT}"
		printf 'http://%s {\n\timport demo\n}\n\n%s {\n\timport demo\n}\n' "${ip}" "${domain}"
	} >"${stage}/Caddyfile"
	python3 "${SCRIPT_DIR}/landing.py" html "${version} (${rev})" "${go_version}" "${java_version}" "${flags}" >"${stage}/www/index.html"
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
	ensure_firewall "${NAME}-web" tcp:80,tcp:443,udp:443 0.0.0.0/0 "Public, unauthenticated access to the Prometheus OpenMetrics 1.0 vs 2.0 scrape demo."
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
	echo "Metrics: https://${domain}/{go,java}/{om1/,om2/,}metrics"
	python3 "${SCRIPT_DIR}/landing.py" links "https://${domain}/"
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

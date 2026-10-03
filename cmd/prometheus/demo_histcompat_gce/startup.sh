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

# [DEMO] Startup script of the Prometheus histogram compat demo VM, see deploy.sh.
#
# It installs gs://<bucket>/releases/<release>.tgz (from the instance metadata),
# runs one prom-demo@<name> systemd service per demo instance on localhost and
# serves them with Caddy. It is idempotent and runs on boot and whenever the
# startup-script, bucket or release metadata changes (see prom-demo-sync).

set -euo pipefail

# Serialize with runs triggered by prom-demo-sync.
exec 9>/run/prom-demo.lock
flock 9

MD="http://metadata.google.internal/computeMetadata/v1"
BASE="/opt/prom-demo"
HASH_FILE="/var/lib/prom-demo-sync.hash"

md() { curl -fsS -H "Metadata-Flavor: Google" "${MD}/$1"; }
log() { echo ">> $*"; }

# install_file moves $1 to $2 with mode $3 and returns 0 if $2 changed.
install_file() {
	if cmp -s "$1" "$2"; then
		rm -f "$1"
		return 1
	fi
	chmod "$3" "$1"
	mv -f "$1" "$2"
}

state_hash() {
	local k
	for k in startup-script bucket release; do
		md "instance/attributes/${k}"
		echo
	done | sha256sum | cut -d' ' -f1
}

install_caddy() {
	local i
	for i in 1 2 3 4 5 6 7 8 9 10; do
		# Retry, as apt can be locked by unattended upgrades right after boot.
		if apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 install -y -qq --no-install-recommends caddy; then
			return 0
		fi
		log "Installing Caddy failed (attempt ${i}), retrying."
		sleep 15
	done
	return 1
}

HASH="$(state_hash)"
BUCKET="$(md instance/attributes/bucket)"
RELEASE="$(md instance/attributes/release)"
restart=0
units_changed=0

cat >/usr/local/bin/prom-demo-sync.new <<'SYNC'
#!/usr/bin/env bash
# Re-runs the startup script when the startup-script, bucket or release metadata changes.
set -euo pipefail
MD="http://metadata.google.internal/computeMetadata/v1/instance/attributes"
want="$(for k in startup-script bucket release; do curl -fsS -H "Metadata-Flavor: Google" "${MD}/${k}"; echo; done | sha256sum | cut -d' ' -f1)"
if [[ "${want}" != "$(cat /var/lib/prom-demo-sync.hash 2>/dev/null || true)" ]]; then
	exec google_metadata_script_runner startup
fi
SYNC
install_file /usr/local/bin/prom-demo-sync.new /usr/local/bin/prom-demo-sync 0755 || true

cat >/etc/systemd/system/prom-demo-sync.service.new <<'UNIT'
[Unit]
Description=Re-run the Prometheus demo startup script on metadata changes

[Service]
Type=oneshot
ExecStart=/usr/local/bin/prom-demo-sync
# Log to the serial console like google-startup-scripts.service, for "deploy.sh logs".
StandardOutput=journal+console
StandardError=journal+console
UNIT

cat >/etc/systemd/system/prom-demo-sync.timer.new <<'UNIT'
[Unit]
Description=Check the Prometheus demo metadata for changes every 30s

[Timer]
OnBootSec=1min
OnUnitActiveSec=30s
AccuracySec=5s

[Install]
WantedBy=timers.target
UNIT

cat >/etc/systemd/system/prom-demo@.service.new <<'UNIT'
[Unit]
Description=Prometheus histogram compat demo instance %i
Wants=network-online.target
After=network-online.target

[Service]
EnvironmentFile=/opt/prom-demo/current/env/%i.env
ExecStart=/opt/prom-demo/current/prometheus \
  --config.file=/opt/prom-demo/current/configs/%i.yml \
  --storage.tsdb.path=%S/prom-demo/%i \
  --web.listen-address=127.0.0.1:${PORT} \
  --web.route-prefix=/ \
  --web.page-title=${TITLE} \
  --web.max-connections=256 \
  --query.timeout=30s \
  --query.max-concurrency=4 \
  --query.max-samples=5000000 \
  $EXTRA_ARGS
Restart=always
RestartSec=5
TimeoutStopSec=60
MemoryMax=512M
DynamicUser=yes
StateDirectory=prom-demo/%i
ProtectHome=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
LockPersonality=yes
NoNewPrivileges=yes
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
UNIT

for unit in prom-demo-sync.service prom-demo-sync.timer prom-demo@.service; do
	if install_file "/etc/systemd/system/${unit}.new" "/etc/systemd/system/${unit}" 0644; then
		units_changed=1
	fi
done
if [[ "${units_changed}" == 1 ]]; then
	systemctl daemon-reload
	restart=1
fi
# Enabled early, so that failed runs are retried until the hash file is written.
systemctl enable --now --quiet prom-demo-sync.timer

if ! command -v caddy >/dev/null; then
	log "Installing Caddy."
	install_caddy
fi

install -d -m 0755 "${BASE}/releases"
dir="${BASE}/releases/${RELEASE}"
if [[ "$(readlink "${BASE}/current" || true)" != "releases/${RELEASE}" ]]; then
	log "Installing release ${RELEASE} from gs://${BUCKET}."
	rm -rf "${dir}.tmp" "${dir}.tgz"
	token="$(md instance/service-accounts/default/token | sed -E 's/.*"access_token" *: *"([^"]+)".*/\1/')"
	curl -fsS --retry 5 -H "Authorization: Bearer ${token}" -o "${dir}.tgz" \
		"https://storage.googleapis.com/${BUCKET}/releases/${RELEASE}.tgz"
	install -d -m 0755 "${dir}.tmp"
	tar -xzf "${dir}.tgz" -C "${dir}.tmp"
	chmod -R a+rX "${dir}.tmp"
	rm -rf "${dir}" "${dir}.tgz"
	mv "${dir}.tmp" "${dir}"
	ln -sfn "releases/${RELEASE}" "${BASE}/current.new"
	mv -Tf "${BASE}/current.new" "${BASE}/current"
	restart=1
	# Keep the current and the 2 newest other releases for debugging.
	for old in $(ls -1dt "${BASE}"/releases/*/ | tail -n +4); do
		[[ "${old}" == "${dir}/" ]] || rm -rf "${old}"
	done
fi

mapfile -t wanted <"${BASE}/current/instances"
for unit in $(systemctl list-units --all --plain --no-legend 'prom-demo@*.service' | awk '{print $1}'); do
	name="${unit#prom-demo@}"
	name="${name%.service}"
	if ! printf '%s\n' "${wanted[@]}" | grep -qxF "${name}"; then
		log "Removing instance ${name}."
		systemctl disable --now "${unit}" || true
	fi
done
# Delete the data of removed (and hence stopped) instances. With DynamicUser,
# systemd keeps the state directories below /var/lib/private and symlinks them.
for dir in /var/lib/private/prom-demo/*/ /var/lib/prom-demo/*/; do
	[[ -e "${dir}" ]] || continue
	name="$(basename "${dir}")"
	if ! printf '%s\n' "${wanted[@]}" | grep -qxF "${name}"; then
		log "Deleting the data of the removed instance ${name}."
		rm -rf "/var/lib/private/prom-demo/${name}" "/var/lib/prom-demo/${name}"
	fi
done
for name in "${wanted[@]}"; do
	systemctl enable --quiet "prom-demo@${name}.service"
	if [[ "${restart}" == 1 ]]; then
		systemctl restart "prom-demo@${name}.service"
	else
		systemctl start "prom-demo@${name}.service"
	fi
done

if ! out="$(HOME=/root caddy validate --adapter caddyfile --config "${BASE}/current/Caddyfile" 2>&1)"; then
	log "Keeping the previous Caddyfile, the new one is invalid: ${out}"
	exit 1
fi
cp "${BASE}/current/Caddyfile" /etc/caddy/Caddyfile.new
if install_file /etc/caddy/Caddyfile.new /etc/caddy/Caddyfile 0644; then
	log "Reloading Caddy."
	systemctl reload-or-restart caddy
fi
systemctl enable --now --quiet caddy

for name in "${wanted[@]}"; do
	port="$(sed -n 's/^PORT=//p' "${BASE}/current/env/${name}.env")"
	for _ in $(seq 60); do
		curl -fsS -m 2 -o /dev/null "http://127.0.0.1:${port}/-/ready" 2>/dev/null && break
		sleep 1
	done
	log "Instance ${name}: $(curl -fsS -m 2 "http://127.0.0.1:${port}/-/ready" 2>&1 || true)"
done

echo "${HASH}" >"${HASH_FILE}"
log "Release ${RELEASE} is active."

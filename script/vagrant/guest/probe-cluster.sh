#!/bin/bash
# Run on cluster node1 as root. Verify the live cluster is on cluster nodes
# (not the builder) and that controller/flynn-host answer.
set -euo pipefail
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
IP="${CLUSTER_IP:-}"
MIN_HOSTS="${MIN_HOSTS:-1}"
BUILDER_IP="${BUILDER_IP:-192.168.57.10}"

if ! command -v flynn-host >/dev/null 2>&1; then
  echo "flynn-host is not installed on this node" >&2
  exit 1
fi

export FLYNN_SKIP_UPDATE_CHECK=1
list="$(flynn-host list 2>/dev/null || true)"
echo "${list}"
count="$(printf '%s\n' "${list}" | awk 'NR>1 && NF>=2 {c++} END{print c+0}')"
if [[ "${count}" -lt "${MIN_HOSTS}" ]]; then
  echo "expected at least ${MIN_HOSTS} cluster hosts, flynn-host list has ${count}" >&2
  exit 1
fi
if printf '%s\n' "${list}" | grep -Fq "${BUILDER_IP}"; then
  echo "builder ${BUILDER_IP} must not be a live cluster member" >&2
  exit 1
fi

if [[ -z "${IP}" ]]; then
  IP="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^192\.168\.57\.' | head -1 || true)"
fi
code=""
if [[ -n "${IP}" ]]; then
  code="$(curl -sk --max-time 5 -o /dev/null -w '%{http_code}' \
    --resolve "controller.${DOMAIN}:443:${IP}" \
    "https://controller.${DOMAIN}/" || true)"
fi
if [[ "${code}" != "200" && "${code}" != "401" ]]; then
  code="$(curl -sk --max-time 5 -o /dev/null -w '%{http_code}' \
    "https://controller.${DOMAIN}/" || true)"
fi
if [[ "${code}" != "200" && "${code}" != "401" ]]; then
  echo "controller did not answer (HTTP ${code:-none})" >&2
  exit 1
fi
echo "controller HTTP ${code} hosts=${count} domain=${DOMAIN}"

# Cluster status app (status.<domain>). LAN clients are on the whitelist.
# Require an overall healthy payload so a rolling reload does not continue
# while Layer 1 services are still coming back.
status_json="$(mktemp)"
trap 'rm -f "${status_json}"' EXIT
scode=""
if [[ -n "${IP}" ]]; then
  scode="$(curl -sk --max-time 5 -o "${status_json}" -w '%{http_code}' \
    -H 'Accept: application/json' \
    --resolve "status.${DOMAIN}:443:${IP}" \
    "https://status.${DOMAIN}/" || true)"
fi
if [[ "${scode}" != "200" ]]; then
  scode="$(curl -sk --max-time 5 -o "${status_json}" -w '%{http_code}' \
    -H 'Accept: application/json' \
    "https://status.${DOMAIN}/" || true)"
fi
if [[ "${scode}" != "200" ]]; then
  echo "status.${DOMAIN} did not answer (HTTP ${scode:-none})" >&2
  exit 1
fi
if ! python3 - "${status_json}" <<'PY'
import json, sys
path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    raw = json.load(f)
data = raw.get("data") or raw
if data.get("status") != "healthy":
    raise SystemExit("cluster status is %s" % (data.get("status") or "unknown",))
PY
then
  echo "status.${DOMAIN} is not healthy" >&2
  exit 1
fi
echo "status.${DOMAIN} healthy"

if command -v flynn >/dev/null 2>&1; then
  add="$(flynn-host cli-add-command 2>/dev/null | grep -E 'flynn cluster(:add| add) ' | tail -1 || true)"
  if [[ -n "${add}" ]]; then
    # shellcheck disable=SC2086
    eval "${add/flynn cluster:add /flynn cluster:add --force }" >/dev/null 2>&1 || true
  fi
  if [[ -f /etc/flynn/admin.env ]]; then
    set -a
    # shellcheck disable=SC1091
    source /etc/flynn/admin.env
    set +a
  fi
  # Missing auth.<domain> in /etc/hosts used to hang here on systemd-resolved.
  timeout 20 flynn login --email "${FLYNN_ADMIN_EMAIL:-admin@${DOMAIN}}" --password "${FLYNN_ADMIN_PASSWORD:-flynn-dev}" >/dev/null 2>&1 || true
  flynn apps --all || echo "flynn apps skipped (CLI not configured on this node)"
fi
echo "cluster probe ok"

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

# Scheduler places tenant jobs after a host reboot. Without it, app-one stays 503
# even when controller.<domain> answers 401.
discoverd_key="$(python3 -c 'import json; print((json.load(open("/etc/flynn/host.json")).get("env") or {}).get("DISCOVERD_AUTH_KEY") or "")' 2>/dev/null || true)"
sched_json="$(mktemp)"
status_json="$(mktemp)"
trap 'rm -f "${status_json}" "${sched_json}"' EXIT
sched_code=""
if [[ -n "${discoverd_key}" ]]; then
  sched_code="$(curl -sf --max-time 3 -o "${sched_json}" -w '%{http_code}' \
    -H "Auth-Key: ${discoverd_key}" \
    "http://127.0.0.1:1111/services/controller-scheduler/instances" || true)"
fi
if [[ "${sched_code}" != "200" ]]; then
  echo "controller-scheduler is not registered (HTTP ${sched_code:-none})" >&2
  exit 1
fi
if ! python3 - "${sched_json}" <<'PY'
import json, sys
raw = json.load(open(sys.argv[1], encoding="utf-8"))
if not isinstance(raw, list) or len(raw) < 1:
    raise SystemExit("no scheduler instances")
PY
then
  echo "controller-scheduler has no instances; tenant apps will not be placed" >&2
  exit 1
fi
echo "controller-scheduler instances ok"

# Cluster status app (status.<domain>). LAN clients are on the whitelist.
# Require an overall healthy payload so a rolling reload does not continue
# while Layer 1 services are still coming back.
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
if [[ "${scode}" != "200" && "${scode}" != "500" ]]; then
  echo "status.${DOMAIN} did not answer (HTTP ${scode:-none})" >&2
  exit 1
fi
# Overall unhealthy is HTTP 500 with a JSON body. Parse it so a rolling reload
# can see which Layer 1 service is down. After one host reboots, status may
# probe a stale controller overlay IP while controller.<domain> already answers.
if ! python3 - "${status_json}" "${scode}" "${code}" <<'PY'
import json, sys
path, scode, controller_http = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path, encoding="utf-8") as f:
    raw = json.load(f)
data = raw.get("data") or raw
overall = data.get("status")
detail = data.get("detail") or {}
optional = {"tarreceive"}
unhealthy = []
if isinstance(detail, dict):
    for name, svc in detail.items():
        st = svc.get("status") if isinstance(svc, dict) else None
        if st and st != "healthy" and name not in optional:
            unhealthy.append(name)
if overall == "healthy":
    raise SystemExit(0)
print("status HTTP %s overall=%s unhealthy=%s" % (
    scode, overall or "unknown", ",".join(unhealthy) or "none"), file=sys.stderr)
if "controller-scheduler" in unhealthy:
    raise SystemExit("controller-scheduler is unhealthy")
if unhealthy == ["controller"] and controller_http in ("200", "401"):
    print("status.detail.controller is unhealthy but controller HTTP %s; treating cluster as settled" % controller_http)
    raise SystemExit(0)
raise SystemExit("cluster status is %s" % (overall or "unknown",))
PY
then
  echo "status.${DOMAIN} is not healthy" >&2
  exit 1
fi
echo "status.${DOMAIN} healthy"

if [[ "${PROBE_QUICK:-}" == "1" ]]; then
  echo "cluster probe ok (quick)"
  exit 0
fi

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
  timeout 20 flynn apps --all || echo "flynn apps skipped (CLI not configured on this node)"
fi
echo "cluster probe ok"

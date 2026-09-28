#!/bin/bash
# Run on the builder as root.
# Exit 0 if the controller answers.
# Exit 2 if the cluster is not bootstrapped (no host.json, a guest-cli DISCOVERD
# stub, or flynn-host started but controller never came back). setup treats 2 as
# "run bootstrap-flynn". Do not exit 1 for a dead leftover cluster — that made
# `vagrant-dev.sh setup` stop after "starting the existing flynn-host".
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

controller_code() {
  local code
  code="$(curl -sk --max-time 3 -o /dev/null -w '%{http_code}' \
    --resolve controller.1.localflynn.com:443:192.0.2.200 \
    https://controller.1.localflynn.com/ || true)"
  if [[ "${code}" == "200" || "${code}" == "401" ]]; then
    printf '%s' "${code}"
    return
  fi
  curl -s --max-time 3 -o /dev/null -w '%{http_code}' \
    --resolve controller.1.localflynn.com:80:192.0.2.200 \
    http://controller.1.localflynn.com/ || true
}

# guest-cli writes DISCOVERD into host.json even when Flynn was removed.
# A real bootstrap persists FLYNN_HOST_AUTH_KEY / AUTH_KEY.
host_json_bootstrapped() {
  python3 - <<'PY'
import json, sys
path = "/etc/flynn/host.json"
try:
    data = json.load(open(path)) or {}
except Exception:
    sys.exit(1)
env = data.get("env") or {}
for key in ("FLYNN_HOST_AUTH_KEY", "AUTH_KEY", "CONTROLLER_KEY"):
    if str(env.get(key) or "").strip():
        sys.exit(0)
sys.exit(1)
PY
}

dump_host_logs() {
  echo "last controller HTTP status: ${1:-none}" >&2
  for f in /tmp/flynn-host-0.log /var/log/flynn/host-0/flynn-host.log; do
    if [[ -f "${f}" ]]; then
      echo "---- ${f} (tail) ----" >&2
      tail -n 40 "${f}" >&2 || true
    fi
  done
}

code="$(controller_code)"
if [[ "${code}" == "200" || "${code}" == "401" ]]; then
  echo "cluster already up"
  exit 0
fi
if [[ ! -f /etc/flynn/host.json ]] || ! host_json_bootstrapped; then
  echo "cluster not bootstrapped"
  exit 2
fi

echo "starting the existing flynn-host"
if ! "${ROOT}/script/start-flynn-host" --no-destroy-vols --no-destroy-state 0; then
  echo "start-flynn-host exited $?; waiting in case a daemon is already running" >&2
fi
for _ in $(seq 1 30); do
  code="$(controller_code)"
  if [[ "${code}" == "200" || "${code}" == "401" ]]; then
    echo "cluster is up"
    exit 0
  fi
  sleep 2
done
echo "flynn-host did not become ready (controller never answered); treating as not bootstrapped" >&2
dump_host_logs "${code}"
exit 2

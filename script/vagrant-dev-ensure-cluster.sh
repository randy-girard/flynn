#!/bin/bash
# Run on the builder as root. Exit 0 if the cluster answers, 2 if it has
# never been bootstrapped. A stopped daemon with /etc/flynn/host.json is
# started again without wiping volumes.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

controller_code() {
  curl -sk --max-time 3 -o /dev/null -w '%{http_code}' \
    --resolve controller.1.localflynn.com:443:192.0.2.200 \
    https://controller.1.localflynn.com/ || true
}

code="$(controller_code)"
if [[ "${code}" == "200" || "${code}" == "401" ]]; then
  echo "cluster already up"
  exit 0
fi
if [[ ! -f /etc/flynn/host.json ]]; then
  echo "cluster not bootstrapped"
  exit 2
fi

echo "starting the existing flynn-host"
"${ROOT}/script/start-flynn-host" --no-destroy-vols --no-destroy-state 0 || true
for _ in $(seq 1 30); do
  code="$(controller_code)"
  if [[ "${code}" == "200" || "${code}" == "401" ]]; then
    echo "cluster is up"
    exit 0
  fi
  sleep 2
done
echo "flynn-host did not become ready" >&2
exit 1

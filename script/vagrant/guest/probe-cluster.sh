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

if command -v flynn >/dev/null 2>&1; then
  add="$(flynn-host cli-add-command 2>/dev/null | grep -E 'flynn cluster(:add| add) ' | tail -1 || true)"
  if [[ -n "${add}" ]]; then
    # shellcheck disable=SC2086
    eval "${add/flynn cluster:add /flynn cluster:add --force }" >/dev/null 2>&1 || true
  fi
  flynn apps --all || echo "flynn apps skipped (CLI not configured on this node)"
fi
echo "cluster probe ok"

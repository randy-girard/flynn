#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
  cat <<USAGE >&2
usage: $(basename "$0") <regex> [run-integration-tests options...]

Runs integration tests matching <regex> (same as: script/run-integration-tests -f <regex>).
Additional flags are forwarded (e.g. -n 3 for cluster size, -s to stream).

Examples:
  $(basename "$0") 'RouterSuite\\.TestAdditionalHttpPorts'
  $(basename "$0") 'MariaDB' -n 3

Slow nested clusters: export TEST_CLUSTER_HOST_TIMEOUT=15m
USAGE
}

if [[ $# -eq 0 ]]; then
  usage
  exit 1
fi
if [[ "${1}" == "-h" || "${1}" == "--help" ]]; then
  usage
  exit 0
fi

filter="$1"
shift
exec "${ROOT}/script/run-integration-tests" -f "${filter}" "$@"

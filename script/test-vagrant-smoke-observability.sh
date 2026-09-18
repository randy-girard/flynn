#!/bin/bash
# Regression: smoke and host gate must cover OpenTelemetry sinks, cluster apex
# routing, and interactive flynn-host fix.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${ROOT}/host/cli/otel.go" 'SinkKindOTLP' "flynn-host otel must create OTLP sinks"
need "${ROOT}/host/logmux/otel.go" '/v1/logs' "OTLP sink must POST JSON logs"
need "${ROOT}/host/logmux/otel.go" '/v1/metrics' "OTLP sink must POST JSON metrics"
need "${ROOT}/controller/types/types.go" 'SinkScopeSystem' "sinks must distinguish system vs app logs"
need "${ROOT}/pkg/plugin/apex.go" 'AssignApex' "cluster apex must be assignable to an app"
need "${ROOT}/host/cli/domain.go" 'domain apex' "flynn-host domain apex must exist"
need "${ROOT}/host/cli/fix.go" '--yes' "flynn-host fix must have a non-interactive --yes flag"
need "${ROOT}/host/fixer/fixer.go" 'isInteractive' "flynn-host fix must prompt on a TTY"
need "${smoke}" 'cli-host-otel' "upgrade smoke must list OTEL sinks"
need "${smoke}" 'cli-host-domain' "upgrade smoke must show the cluster apex"
need "${smoke}" 'cli-host-fix-help' "upgrade smoke must show flynn-host fix --yes"

echo "ok OpenTelemetry, apex domain, and interactive fix are covered"

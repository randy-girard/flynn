#!/bin/bash
# Regression: smoke and host gate must cover the optional OpenTelemetry plugin,
# cluster apex routing, and interactive flynn-host fix.
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

need "${ROOT}/host/cli/otel.go" 'plugin:install otel' "flynn-host otel must require the otel plugin"
need "${ROOT}/host/cli/otel.go" '/exporters' "flynn-host otel must talk to the plugin exporter API"
if [[ -f "${ROOT}/host/logmux/otel.go" ]]; then
  echo "OTLP export must live in the otel plugin, not flynn-host logmux" >&2
  exit 1
fi
need "${ROOT}/controller/types/types.go" 'SinkScopeSystem' "sinks must distinguish system vs app logs"
need "${ROOT}/pkg/plugin/apex.go" 'AssignApex' "cluster apex must be assignable to an app"
need "${ROOT}/host/cli/domain.go" 'domain:apex' "flynn-host domain:apex must exist"
need "${ROOT}/host/cli/fix.go" '--yes' "flynn-host fix must have a non-interactive --yes flag"
need "${ROOT}/host/fixer/fixer.go" 'isInteractive' "flynn-host fix must prompt on a TTY"
need "${smoke}" 'cli-host-otel' "upgrade smoke must list OTEL exporters"
need "${smoke}" 'probe_otel_export' "upgrade smoke must prove the otel plugin POSTs OTLP metrics"
need "${smoke}" 'FLYNN_PLUGIN_SETUP_OTEL_ENDPOINT' "otel install must point at the dummy collector via setup env"
need "${smoke}" '/v1/metrics' "dummy collector must accept OTLP/HTTP /v1/metrics"
need "${smoke}" 'otel-smoke-metrics' "dummy collector must persist posted OTLP payloads"
need "${smoke}" 'nohup python3' "dummy collector must survive the SSH session that starts it"
need "${smoke}" 'Sync plugin VM mounts' "plugin mounts must attach before Flynn install"
need "${smoke}" 'cli-host-domain' "upgrade smoke must show the cluster apex"
need "${smoke}" 'cli-host-fix-help' "upgrade smoke must show flynn-host fix --yes"
need "${smoke}" 'discovery otel' "upgrade smoke must install the otel plugin"

echo "ok OpenTelemetry plugin, apex domain, and interactive fix are covered"

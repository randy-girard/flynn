#!/bin/bash
# Regression: smoke must create a pipeline, attach staging + an undeployed
# production app, and promote after the pipeline plugin is installed.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
example="${ROOT}/smoke-matrix.example.yaml"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${ROOT}/pkg/plugin/official-plugins.json" '"name": "pipeline"' \
  "official catalog must include the pipeline plugin"
need "${smoke}" 'PLUGIN_SMOKE_APPS:-redis mysql mongodb kafka clickhouse dashboard www discovery otel scheduler pipeline' \
  "default plugin install list must include pipeline"
need "${smoke}" 'pipeline_smoke_wanted' \
  "upgrade smoke must gate pipeline probes on PLUGIN_SMOKE_APPS"
need "${smoke}" 'probe_pipeline_promote' \
  "upgrade smoke must promote through the pipeline plugin after apps exist"
need "${smoke}" 'pipeline:create' \
  "pipeline smoke must create a named pipeline"
need "${smoke}" 'pipeline:add' \
  "pipeline smoke must attach staging and production apps"
need "${smoke}" 'pipeline:promote' \
  "pipeline smoke must promote the uploaded app into production"
need "${smoke}" 'pipeline_prod_app' \
  "pipeline smoke must use a dedicated undeployed production app"
need "${smoke}" 'cli-pipeline-dest-release' \
  "pipeline smoke must require a release on the destination after promote"
need "${smoke}" 'cli-pipeline-dest-ps' \
  "first promote into an empty app must scale processes, not leave a no-op deploy"
need "${smoke}" '401|unexpected status|missing user credentials' \
  "pipeline smoke must fail on controller 401 / missing user credentials"
need "${smoke}" 'cli-help-pipeline' \
  "CLI step must show pipeline in flynn help after plugin install"
need "${example}" 'id: pipeline' \
  "example matrix must include a pipeline promote configuration"

if awk '/^step_install_plugins\(/,/^}/' "${smoke}" | grep -q 'probe_pipeline_promote'; then
  echo "pipeline promote cannot run at plugin-install time; apps are not deployed yet" >&2
  exit 1
fi
if ! awk '/^step_cli_functions\(/,/^}/' "${smoke}" | grep -q 'probe_pipeline_promote'; then
  echo "pipeline promote must run in the live CLI step after apps are uploaded" >&2
  exit 1
fi

echo "ok pipeline plugin promote smoke is covered"

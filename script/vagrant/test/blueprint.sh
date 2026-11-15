#!/bin/bash
# Regression: smoke installs the blueprint plugin with the other first-party
# app plugins, and the official catalog can resolve its GitHub repo.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
smoke="${ROOT}/script/vagrant/suite.sh"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${ROOT}/pkg/plugin/official-plugins.json" '"name": "blueprint"' \
  "official catalog must include the blueprint plugin"
need "${ROOT}/pkg/plugin/official-plugins.json" '"repo": "flynn-plugin-blueprint"' \
  "official catalog must point blueprint at flynn-plugin-blueprint"
need "${smoke}" 'PLUGIN_SMOKE_APPS:-redis mysql mongodb kafka clickhouse dashboard www discovery otel scheduler pipeline blueprint' \
  "default plugin install list must include blueprint"
need "${smoke}" 'probe_delegated_plugin_cli_visible' \
  "smoke must show flynn blueprint after a plugin with a user CLI is installed"
need "${ROOT}/docs/content/plugins.md" 'flynn-plugin-blueprint' \
  "plugin docs must name the blueprint checkout"
need "${ROOT}/docs/content/cli.md" 'blueprint:provision' \
  "CLI docs must list blueprint provision"

echo "ok blueprint plugin is in the catalog and default smoke install"

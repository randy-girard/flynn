#!/bin/bash
# Regression: flynn-host plugin uninstall must reverse install generically
# (app, routes via DeleteApp, webhooks, optional hooks.uninstall) without a
# plugin-name switch. Resource providers with leftover resources refuse unless
# --force.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
uninstall="${ROOT}/pkg/plugin/uninstall.go"
uninstall_test="${ROOT}/pkg/plugin/uninstall_test.go"
install="${ROOT}/pkg/plugin/install.go"
cli="${ROOT}/host/cli/plugin.go"
cli_test="${ROOT}/host/cli/plugin_test.go"
docs="${ROOT}/docs/content/plugins.md"

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_in() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file "${uninstall}" "plugin uninstall must exist"
need_file "${uninstall_test}" "plugin uninstall tests must exist"
need_file "${cli}" "flynn-host plugin CLI must exist"
need_file "${docs}" "plugin docs must exist"

need_in "${cli}" 'plugin uninstall' \
  "flynn-host plugin must expose uninstall"
need_in "${cli}" 'plugin update' \
  "flynn-host plugin must expose update"
need_in "${cli}" '--force' \
  "uninstall must expose --force for resource providers still in use"
need_in "${cli_test}" 'TestPluginUninstallUsage' \
  "docopt tests must cover flynn-host plugin uninstall"
need_in "${cli_test}" 'TestPluginUpdateUsage' \
  "docopt tests must cover flynn-host plugin update"
need_in "${uninstall}" 'func \(in \*Installer\) Uninstall' \
  "Installer.Uninstall must exist"
need_in "${uninstall}" 'ensureProviderUnused' \
  "uninstall must refuse resource providers with leftover resources"
need_in "${uninstall}" 'runUninstallHook' \
  "uninstall must run optional hooks.uninstall"
need_in "${uninstall}" 'removePluginWebhooks' \
  "uninstall must remove plugin-registered host webhooks"
need_in "${uninstall}" 'DeleteApp' \
  "uninstall must delete the plugin app (routes go with DeleteApp)"
need_in "${uninstall_test}" 'TestUninstallDeletesPluginApp' \
  "unit tests must delete the plugin app"
need_in "${uninstall_test}" 'TestUninstallRefusesProviderWithResources' \
  "unit tests must refuse resource-provider uninstall while resources remain"
need_in "${uninstall_test}" 'TestUninstallRunsHook' \
  "unit tests must run hooks.uninstall"
need_in "${uninstall_test}" 'TestUninstallHookMissingFails' \
  "declared uninstall hooks must fail when the script is missing"
need_in "${uninstall_test}" 'TestUninstallRemovesPluginWebhooks' \
  "unit tests must remove plugin webhooks by ID prefix"
need_in "${install}" 'uninstallHook' \
  "manifest must expose hooks.uninstall"
need_in "${install}" 'previousReleaseScaleDown' \
  "plugin reinstall must scale the previous release to zero"
need_in "${install}" 'deployHook' \
  "plugin update must run hooks.upgrade instead of hooks.install"
need_in "${docs}" 'plugin uninstall' \
  "plugin docs must describe flynn-host plugin uninstall"

if grep -nE 'Name[[:space:]]*==[[:space:]]*"dashboard"|name[[:space:]]*==[[:space:]]*"dashboard"' "${uninstall}" "${install}"; then
  echo "plugin uninstall must not special-case dashboard by name" >&2
  exit 1
fi

echo "ok flynn-host plugin uninstall reverses install generically"

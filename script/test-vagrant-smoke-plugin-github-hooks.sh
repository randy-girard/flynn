#!/bin/bash
# Regression: GitHub plugin install unpacks release assets, not a git
# checkout. Declared hooks.install files must be published and fetched so
# smoke would have caught a missing script/install.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
github="${ROOT}/pkg/plugin/github.go"
github_test="${ROOT}/pkg/plugin/github_test.go"
install="${ROOT}/pkg/plugin/install.go"
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
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file "${github}" "GitHub plugin fetch must exist"
need_file "${github_test}" "GitHub plugin fetch tests must exist"
need_file "${install}" "plugin installer must exist"
need_file "${docs}" "plugin docs must exist"

need_in "${github}" 'fetchGitHubHooks' \
  "GitHub plugin install must download declared hook scripts from release assets"
need_in "${github}" 'HookAssetNames' \
  "hook release assets must use flat names (script/install.sh → script-install.sh)"
need_in "${github}" 'script-install.sh' \
  "GitHub asset naming must document script-install.sh"
need_in "${github}" 'script-uninstall.sh' \
  "GitHub asset naming must document script-uninstall.sh"
need_in "${github_test}" 'TestFetchGitHubReleaseHooks' \
  "unit tests must fetch hooks.install from GitHub release assets"
need_in "${github_test}" 'TestFetchGitHubReleaseMissingHookAsset' \
  "unit tests must fail GitHub install when a declared hook asset is missing"
need_in "${install}" 'hook %s' \
  "runHook must still fail when the hook file is missing (do not skip)"
need_in "${smoke}" 'assemble_plugin_github_unpack' \
  "smoke must install plugins from a GitHub-style unpack, not the git checkout"
need_in "${smoke}" 'dist/github-unpack' \
  "smoke GitHub unpack must live under dist/ so it is not the sibling checkout"
need_in "${smoke}" 'GitHub unpack missing hook asset' \
  "smoke unpack must fail if plugin-build did not copy hooks into dist/"
need_in "${smoke}" 'must not include the plugin git checkout' \
  "smoke must refuse a GitHub unpack that still has cmd/ or .git"
need_in "${smoke}" 'script/install.sh → script-install.sh' \
  "plugin_dist_ready must require declared hooks as flat dist assets"
need_in "${docs}" 'script-install.sh' \
  "plugin docs must tell authors to publish hook scripts as release assets"

if grep -nE 'skipping hooks\.install|skipMissing' "${github}" "${install}"; then
  echo "GitHub plugin install must run declared hooks, not skip a missing script" >&2
  exit 1
fi

if grep -nE 'Name[[:space:]]*==[[:space:]]*"dashboard"|name[[:space:]]*==[[:space:]]*"dashboard"' "${github}" "${install}"; then
  echo "plugin GitHub hook fetch must not special-case dashboard by name" >&2
  exit 1
fi

echo "ok GitHub plugin install fetches and runs declared hooks"

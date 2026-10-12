#!/bin/bash
# Run on a running cluster node as root. Installs a sibling plugin checkout
# (/opt/flynn-plugins/<checkout>), or updates it when the manifest's app is
# already installed. Both rebuild dist/ so local source changes ship.
set -euo pipefail

dir="${1:-}"
if [[ -z "${dir}" ]]; then
  echo "usage: plugin.sh /opt/flynn-plugins/<checkout>" >&2
  exit 1
fi
manifest="${dir}/flynn-plugin.json"
if [[ ! -f "${manifest}" ]]; then
  echo "${manifest} is not visible on this node." >&2
  echo "A checkout added after the VMs booted needs script/vagrant.sh reload to sync it." >&2
  exit 1
fi
app="$(jq -r '.app.name // empty' "${manifest}")"
if [[ -z "${app}" ]]; then
  echo "${manifest} has no app.name" >&2
  exit 1
fi

yes=()
if [[ "${FLYNN_VAGRANT_YES:-0}" == "1" ]]; then
  yes=(--yes)
fi

installed="$(flynn-host plugin:list)"
if awk '$1 != "NAME" { print $1 }' <<<"${installed}" | grep -Fxq "${app}"; then
  echo "${app} is installed; updating from ${dir}"
  exec flynn-host plugin:update --rebuild ${yes[@]+"${yes[@]}"} "${dir}"
fi
echo "${app} is not installed; installing from ${dir}"
exec flynn-host plugin:install --rebuild ${yes[@]+"${yes[@]}"} "${dir}"

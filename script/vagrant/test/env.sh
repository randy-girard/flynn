#!/bin/bash
# Contract for smoke VM lifecycle. Does not boot a VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
entry="${ROOT}/script/vagrant-smoke.sh"
script="${ROOT}/script/vagrant/smoke-env.sh"
mod="${ROOT}/script/vagrant"

if [[ ! -x "${script}" ]]; then
  echo "script/vagrant/smoke-env.sh must exist" >&2
  exit 1
fi
if ! grep -Fq 'vagrant/smoke-env.sh' "${entry}"; then
  echo "vagrant-smoke.sh must dispatch lifecycle commands to script/vagrant/smoke-env.sh" >&2
  exit 1
fi

for want in \
  "flynn_vagrant_use smoke" \
  "stop|halt)" \
  "destroy|teardown)" \
  "reload|restart)" \
  "builder" \
  "node"; do
  if ! grep -Fq "${want}" "${script}"; then
    echo "smoke-env.sh missing ${want}" >&2
    exit 1
  fi
done
if ! grep -Fq 'FLYNN_VAGRANT_ENV=smoke' "${mod}/lib/env.sh"; then
  echo "lib/env.sh must set FLYNN_VAGRANT_ENV=smoke" >&2
  exit 1
fi
if ! grep -Fq 'VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant"' "${mod}/lib/env.sh"; then
  echo "smoke must use .vagrant" >&2
  exit 1
fi
if ! grep -Fq 'vagrant halt' "${mod}/lib/lifecycle.sh"; then
  echo "shared lifecycle must halt with vagrant halt" >&2
  exit 1
fi
if ! grep -Fq 'vagrant destroy -f' "${mod}/lib/lifecycle.sh"; then
  echo "shared lifecycle must destroy with vagrant destroy -f" >&2
  exit 1
fi
if ! grep -Fq 'vagrant reload --no-provision' "${mod}/lib/lifecycle.sh"; then
  echo "shared lifecycle must reload with vagrant reload --no-provision" >&2
  exit 1
fi
if grep -Fq 'VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant-dev"' "${script}"; then
  echo "smoke-env.sh must use .vagrant, not .vagrant-dev" >&2
  exit 1
fi
if grep -Fq 'build-dev' "${script}"; then
  echo "smoke lifecycle must keep Flynn artifacts in ./build, not ./build-dev" >&2
  exit 1
fi
if ! grep -Fq 'vagrant/suite.sh' "${entry}"; then
  echo "vagrant-smoke.sh must still exec the acceptance suite" >&2
  exit 1
fi
if ! grep -Fq 'status|ssh|up|reload|restart|stop|halt|destroy|teardown|env)' "${entry}"; then
  echo "vagrant-smoke.sh must intercept lifecycle subcommands before the suite" >&2
  exit 1
fi
help="$(bash "${script}" help)"
for want in "status" "ssh" "up" "reload" "restart" "stop" "halt" "destroy" "teardown" "script/vagrant-smoke.sh --item quick" "script/vagrant-smoke.sh --item datastores" "make vagrant-smoke"; do
  if ! grep -Fq "${want}" <<<"${help}"; then
    echo "smoke env help missing ${want}" >&2
    exit 1
  fi
done
echo "ok"

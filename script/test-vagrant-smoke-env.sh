#!/bin/bash
# Contract for smoke VM lifecycle. Does not boot a VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
entry="${ROOT}/script/vagrant-smoke.sh"
script="${ROOT}/script/vagrant-smoke-env.sh"
for want in \
  "FLYNN_VAGRANT_ENV=smoke" \
  ".vagrant" \
  "vagrant halt" \
  "vagrant destroy -f" \
  "vagrant reload --no-provision" \
  "stop|halt)" \
  "destroy|teardown)" \
  "reload|restart)" \
  "builder" \
  "node"; do
  if ! grep -Fq "${want}" "${script}"; then
    echo "vagrant-smoke-env.sh missing ${want}" >&2
    exit 1
  fi
done
if grep -Fq 'VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant-dev"' "${script}"; then
  echo "vagrant-smoke-env.sh must use .vagrant, not .vagrant-dev" >&2
  exit 1
fi
if grep -Fq 'build-dev' "${script}"; then
  echo "smoke lifecycle must keep Flynn artifacts in ./build, not ./build-dev" >&2
  exit 1
fi
if ! grep -Fq 'vagrant-smoke-env.sh' "${entry}"; then
  echo "vagrant-smoke.sh must dispatch lifecycle commands to vagrant-smoke-env.sh" >&2
  exit 1
fi
if ! grep -Fq 'vagrant-upgrade-smoke.sh' "${entry}"; then
  echo "vagrant-smoke.sh must still exec the acceptance suite" >&2
  exit 1
fi
if ! grep -Fq 'status|ssh|up|reload|restart|stop|halt|destroy|teardown|env)' "${entry}"; then
  echo "vagrant-smoke.sh must intercept lifecycle subcommands before the suite" >&2
  exit 1
fi
help="$(bash "${script}" help)"
for want in "status" "ssh" "up" "reload" "restart" "stop" "halt" "destroy" "teardown" "script/vagrant-smoke.sh --item quick"; do
  if ! grep -Fq "${want}" <<<"${help}"; then
    echo "smoke env help missing ${want}" >&2
    exit 1
  fi
done
echo "ok"

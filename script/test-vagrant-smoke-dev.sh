#!/bin/bash
# Contract for the laptop Vagrant loop. Does not boot a VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
script="${ROOT}/script/vagrant-dev.sh"
for want in \
  "FLYNN_VAGRANT_ENV=dev" \
  ".vagrant-dev" \
  "dev-builder" \
  "ensure_dev_vm" \
  "sudo -n bash -lc" \
  "script/bootstrap-flynn" \
  "vagrant-dev-restore-layers.sh" \
  "vagrant-dev-mac.sh" \
  "vagrant-dev-cli.sh" \
  "vagrant-dev-update.sh" \
  "./build.sh cluster" \
  "build-dev" \
  "GOMEMLIMIT" \
  "setup)"; do
  if ! grep -Fq "${want}" "${script}"; then
    echo "vagrant-dev.sh missing ${want}" >&2
    exit 1
  fi
done
if grep -Fq 'vagrant up builder' "${script}"; then
  echo "vagrant-dev.sh must not boot the smoke builder" >&2
  exit 1
fi
if ! grep -Fq "flynn-host update" "${ROOT}/script/vagrant-dev-update.sh"; then
  echo "vagrant-dev-update.sh must run flynn-host update" >&2
  exit 1
fi
if ! grep -Fq 'build-dev/bin' "${ROOT}/script/vagrant-dev-cli.sh"; then
  echo "vagrant-dev-cli.sh must write the laptop CLI into build-dev, not smoke's build/" >&2
  exit 1
fi
if grep -Eq 'vagrant up node' "${script}"; then
  echo "vagrant-dev.sh must not boot cluster nodes" >&2
  exit 1
fi
if ! grep -Fq 'vagrant reload --no-provision' "${script}"; then
  echo "vagrant-dev.sh reload must use vagrant reload --no-provision" >&2
  exit 1
fi
if ! grep -Fq 'reload|restart)' "${script}"; then
  echo "vagrant-dev.sh must accept reload and restart" >&2
  exit 1
fi
if ! grep -Fq 'machines/dev-node' "${script}"; then
  echo "vagrant-dev.sh must discover extra hosts from .vagrant-dev" >&2
  exit 1
fi
if ! grep -Fq 'start_existing_cluster' "${script}"; then
  echo "vagrant-dev.sh reload must start an existing cluster without bootstrapping" >&2
  exit 1
fi
if ! grep -Fq 'vagrant halt' "${script}"; then
  echo "vagrant-dev.sh stop must use vagrant halt" >&2
  exit 1
fi
if ! grep -Fq 'vagrant destroy -f' "${script}"; then
  echo "vagrant-dev.sh destroy must use vagrant destroy -f" >&2
  exit 1
fi
if ! grep -Fq 'stop|halt)' "${script}"; then
  echo "vagrant-dev.sh must accept stop and halt" >&2
  exit 1
fi
if ! grep -Fq 'destroy|teardown)' "${script}"; then
  echo "vagrant-dev.sh must accept destroy and teardown" >&2
  exit 1
fi
if ! grep -q 'flynn_path_is_mount' "${ROOT}/script/clean-flynn"; then
  echo "clean-flynn must not rm -rf guest build/ when it is the build-dev mount" >&2
  exit 1
fi
if ! grep -Fq 'INVOCATION_ID' "${ROOT}/host/http.go"; then
  echo "ConfigureAuthKey must detect systemd via INVOCATION_ID so vagrant-dev (start-stop-daemon) applies auth in-process" >&2
  exit 1
fi
if ! grep -Fq 'enableAuthInProcess' "${ROOT}/host/http.go"; then
  echo "ConfigureAuthKey must apply the host auth key in-process when flynn-host is not a systemd unit" >&2
  exit 1
fi
if ! grep -Fq 'start-stop-daemon' "${ROOT}/script/start-flynn-host"; then
  echo "start-flynn-host must keep start-stop-daemon (vagrant-dev is not systemd flynn-host)" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_DEV_PIN=' "${ROOT}/script/vagrant-dev-creds.sh"; then
  echo "vagrant-dev-creds.sh must print labeled FLYNN_DEV_PIN so ssh banners cannot steal line 1" >&2
  exit 1
fi
if ! grep -Fq 'parse_dev_creds' "${script}"; then
  echo "vagrant-dev.sh must parse labeled creds, not ssh stdout line 1/2" >&2
  exit 1
fi
if ! grep -Fq 'CAClient' "${ROOT}/cli/cluster.go"; then
  echo "cluster:add must fetch /ca-cert without the cluster key" >&2
  exit 1
fi
if ! grep -Fq 'vagrant-dev-guest-cli.sh' "${ROOT}/script/vagrant-dev-publish.sh"; then
  echo "vagrant-dev publish must install flynn-host on the guest PATH" >&2
  exit 1
fi
if ! grep -Fq '192.0.2.200:1111' "${ROOT}/script/vagrant-dev-guest-cli.sh"; then
  echo "guest flynn-host wrapper must set DISCOVERD to the TEST-NET listen IP" >&2
  exit 1
fi
if ! grep -Fq 'discoverd.DefaultClient = discoverd.NewClient()' "${ROOT}/host/host.go"; then
  echo "flynn-host CLI must recreate DefaultClient after loading host.json DISCOVERD" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_SKIP_UPDATE_CHECK' "${ROOT}/script/vagrant-dev-mac.sh"; then
  echo "vagrant-dev-mac.sh must skip the CLI update nag during cluster:add" >&2
  exit 1
fi
sample=$'Welcome to Ubuntu\nFLYNN_DEV_PIN=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nFLYNN_DEV_KEY=0123456789abcdef0123456789abcdef\n'
pin="$(printf '%s\n' "${sample}" | sed -n 's/^FLYNN_DEV_PIN=//p' | tail -1)"
key="$(printf '%s\n' "${sample}" | sed -n 's/^FLYNN_DEV_KEY=//p' | tail -1)"
if [[ "${pin}" != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" || "${key}" != "0123456789abcdef0123456789abcdef" ]]; then
  echo "labeled creds parse failed: pin=${pin} key=${key}" >&2
  exit 1
fi
bash "${ROOT}/script/test-clean-flynn.sh"
help="$(bash "${script}" help)"
for want in "setup" "cli" "bootstrap" "update" "reload" "restart" "stop" "destroy" "teardown" "build-dev" "flynn -c local apps"; do
  if ! grep -Fq "${want}" <<<"${help}"; then
    echo "help missing ${want}" >&2
    exit 1
  fi
done
echo "ok"

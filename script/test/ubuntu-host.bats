#!/usr/bin/env bats

load "helper"

@test "install scripts require Ubuntu 24.04 only" {
  for f in \
    "${ROOT}/script/install-flynn" \
    "${ROOT}/script/install-flynn.tmpl" \
    "${ROOT}/script/install-flynn-release"
  do
    grep -q 'Ubuntu 24.04' "${f}"
    if grep -E '16\.04|18\.04|is_ubuntu_xenial|is_ubuntu_bionic' "${f}"; then
      echo "${f} must not accept Ubuntu 16.04 or 18.04" >&2
      return 1
    fi
  done
  if grep -q xenial "${ROOT}/script/configure-docker"; then
    echo "configure-docker must not special-case xenial/upstart" >&2
    return 1
  fi
  grep -q 'systemctl restart docker' "${ROOT}/script/configure-docker"
}

@test "install scripts lock public ingress to 22/80/443 and rotate Flynn logs" {
  for f in \
    "${ROOT}/script/install-flynn" \
    "${ROOT}/script/install-flynn.tmpl" \
    "${ROOT}/script/install-flynn-release"
  do
    grep -q 'ufw allow "${port}/tcp"' "${f}"
    grep -q 'ufw default deny incoming' "${f}"
    grep -q 'copytruncate' "${f}"
    grep -q '/etc/logrotate.d' "${f}"
    grep -q -- '--no-firewall' "${f}"
    grep -q -- '--no-logrotate' "${f}"
  done
}

@test "install --clean unmounts overlay and squashfs under /var/lib/flynn" {
  for f in \
    "${ROOT}/script/install-flynn" \
    "${ROOT}/script/install-flynn.tmpl" \
    "${ROOT}/script/install-flynn-release"
  do
    grep -q 'unmount_under_flynn' "${f}"
    grep -q '/var/lib/flynn' "${f}"
  done
}

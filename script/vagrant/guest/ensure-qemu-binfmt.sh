#!/bin/bash
# On aarch64 Flynn Vagrant nodes, register qemu-user-static so the host kernel
# can exec x86_64 ELF files inside Flynn jobs (Heroku buildpack helpers and
# linux-x64 Node). The F-flag interpreter lives on this VM; slugbuilder still
# needs amd64 glibc from builder/img/amd64-qemu-userland.sh. No-op on amd64.
#
# Ubuntu 24.04's qemu-user 8.2 crashes Node with:
#   x86_64-binfmt-P: QEMU internal SIGSEGV {code=MAPERR, addr=0x20}
# qemu-user >= 9 is required. After replacing the binary, re-register binfmt
# so the kernel's F-flag fd is not the old interpreter.
set -euo pipefail

# Minimum qemu-user major that can run Node 24 under binfmt (8.2 SIGSEGVs).
QEMU_MIN_MAJOR=9
# Debian 13+ moved the static emulators into qemu-user; qemu-user-static is a
# tiny transitional package with no qemu-x86_64-static binary.
QEMU_DEB_URL="${FLYNN_QEMU_USER_STATIC_DEB:-https://deb.debian.org/debian/pool/main/q/qemu/qemu-user_10.0.13+ds-0+deb13u1_arm64.deb}"

case "$(uname -m)" in
  aarch64 | arm64) ;;
  *)
    echo "ensure-qemu-binfmt: $(uname -m) does not need x86_64 binfmt"
    exit 0
    ;;
esac

qemu_bin() {
  if command -v qemu-x86_64-static >/dev/null 2>&1; then
    command -v qemu-x86_64-static
  elif command -v qemu-x86_64 >/dev/null 2>&1; then
    command -v qemu-x86_64
  else
    return 1
  fi
}

qemu_major() {
  local line bin
  bin="$(qemu_bin 2>/dev/null || true)"
  if [[ -z "${bin}" ]]; then
    printf '%s' 0
    return
  fi
  line="$("${bin}" --version 2>/dev/null | head -n1 || true)"
  if [[ "${line}" =~ ([0-9]+)\.[0-9]+ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  else
    printf '%s' 0
  fi
}

qemu_new_enough() {
  qemu_bin >/dev/null 2>&1 || return 1
  local major
  major="$(qemu_major)"
  [[ "${major}" -ge "${QEMU_MIN_MAJOR}" ]]
}

install_qemu_apt() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y --no-install-recommends qemu-user-static binfmt-support curl ca-certificates
}

install_qemu_from_deb() {
  local tmp bin wrap
  tmp="$(mktemp -d)"
  echo "ensure-qemu-binfmt: Ubuntu qemu $(qemu_major) is too old for Node; installing >= ${QEMU_MIN_MAJOR} from ${QEMU_DEB_URL}"
  curl -fsSL --retry 5 --retry-delay 3 -o "${tmp}/qemu.deb" "${QEMU_DEB_URL}"
  dpkg-deb -x "${tmp}/qemu.deb" "${tmp}/root"
  bin="$(find "${tmp}/root" -name 'qemu-x86_64-static' -type f | head -n1)"
  if [[ -z "${bin}" ]]; then
    bin="$(find "${tmp}/root" -name 'qemu-x86_64' -type f | head -n1)"
  fi
  if [[ -z "${bin}" ]]; then
    echo "ensure-qemu-binfmt: deb missing qemu-x86_64 / qemu-x86_64-static" >&2
    find "${tmp}/root" -name 'qemu-*' | head -n20 >&2 || true
    rm -rf "${tmp}"
    exit 1
  fi
  # Ubuntu binfmt wrapper is a symlink to qemu-x86_64-static; keep that name.
  install -m 755 "${bin}" /usr/bin/qemu-x86_64-static
  install -m 755 "${bin}" /usr/bin/qemu-x86_64
  wrap="$(find "${tmp}/root" -path '*/qemu-binfmt/x86_64-binfmt-P' -type f | head -n1)"
  if [[ -n "${wrap}" ]]; then
    install -D -m 755 "${wrap}" /usr/libexec/qemu-binfmt/x86_64-binfmt-P
  elif [[ -e /usr/libexec/qemu-binfmt/x86_64-binfmt-P ]]; then
    ln -sfn /usr/bin/qemu-x86_64-static /usr/libexec/qemu-binfmt/x86_64-binfmt-P
  fi
  rm -rf "${tmp}"
}

register_binfmt() {
  if [[ ! -d /proc/sys/fs/binfmt_misc ]]; then
    mkdir -p /proc/sys/fs/binfmt_misc
  fi
  if ! mountpoint -q /proc/sys/fs/binfmt_misc 2>/dev/null; then
    mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc || true
  fi
  # Drop stale F-flag interpreters so the kernel opens the new qemu binary.
  for name in qemu-x86_64 qemu-x86_64-static; do
    if [[ -f "/proc/sys/fs/binfmt_misc/${name}" ]]; then
      echo -1 >"/proc/sys/fs/binfmt_misc/${name}" || true
    fi
  done
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now systemd-binfmt.service >/dev/null 2>&1 || true
    systemctl restart systemd-binfmt.service >/dev/null 2>&1 || true
  fi
  if command -v update-binfmts >/dev/null 2>&1; then
    update-binfmts --import qemu-x86_64 >/dev/null 2>&1 \
      || update-binfmts --enable qemu-x86_64 >/dev/null 2>&1 \
      || true
  fi
}

install_qemu_apt
if ! qemu_new_enough; then
  install_qemu_from_deb
fi
if ! qemu_new_enough; then
  echo "ensure-qemu-binfmt: qemu-x86_64-static is still older than ${QEMU_MIN_MAJOR}" >&2
  qemu-x86_64-static --version >&2 || true
  exit 1
fi
register_binfmt

binfmt=""
for name in qemu-x86_64 qemu-x86_64-static; do
  if [[ -f "/proc/sys/fs/binfmt_misc/${name}" ]]; then
    binfmt="/proc/sys/fs/binfmt_misc/${name}"
    break
  fi
done
if [[ -z "${binfmt}" ]]; then
  echo "ensure-qemu-binfmt: qemu-x86_64 binfmt is not registered" >&2
  ls -la /proc/sys/fs/binfmt_misc/ >&2 || true
  exit 1
fi
if ! grep -q '^enabled' "${binfmt}"; then
  echo "ensure-qemu-binfmt: ${binfmt} is not enabled" >&2
  cat "${binfmt}" >&2
  exit 1
fi
if ! qemu_bin >/dev/null 2>&1; then
  echo "ensure-qemu-binfmt: qemu-x86_64-static is not on PATH" >&2
  exit 1
fi
echo "ensure-qemu-binfmt: ${binfmt} enabled qemu $(qemu_major).x on $(uname -m)"

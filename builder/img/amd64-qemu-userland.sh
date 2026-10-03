#!/bin/bash
# Sourced from heroku-24.sh. On aarch64, install an amd64 glibc userland so
# qemu-user-static registered on the Flynn host (Vagrant nodes) can exec
# dynamically linked x86_64 binaries. Classic Heroku Node ships
# resolve-version-linux (static musl) plus linux-x64 Node (glibc); the host
# binfmt translator is not enough without /lib64/ld-linux-x86-64.so.2 in
# this image. No-op on amd64.
#
# Do not install qemu-user-static here: Flynn jobs cannot register binfmt
# (/proc/sys is read-only). The interpreter must be the host kernel's F-flag
# qemu from script/vagrant/guest/ensure-qemu-binfmt.sh.

flynn_amd64_qemu_userland_native_arch() {
  case "$(uname -m)" in
    aarch64 | arm64) printf '%s' arm64 ;;
    x86_64 | amd64) printf '%s' amd64 ;;
    *) uname -m ;;
  esac
}

case "$(uname -m)" in
  aarch64 | arm64) ;;
  *)
    echo "amd64-qemu-userland: $(uname -m); skipping"
    return 0 2>/dev/null || exit 0
    ;;
esac

export DEBIAN_FRONTEND=noninteractive

native_arch="$(flynn_amd64_qemu_userland_native_arch)"
keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg
if [[ ! -f "${keyring}" ]]; then
  echo "amd64-qemu-userland: missing ${keyring}" >&2
  exit 1
fi

# Pin existing Ubuntu/PGDG sources to the native arch. dpkg --add-architecture
# amd64 would otherwise make apt fetch amd64 from ports.ubuntu.com (404).
flynn_pin_deb822_native_arch() {
  local src=$1
  local tmp
  [[ -f "${src}" ]] || return 0
  [[ "$(basename "${src}")" == ubuntu-amd64.sources ]] && return 0
  tmp="$(mktemp)"
  awk -v arch="${native_arch}" '
    BEGIN { RS = ""; ORS = "\n\n" }
    {
      block = $0
      gsub(/\r/, "", block)
      if (block ~ /^[[:space:]]*$/) next
      if (block ~ /Types:/ && block !~ /Architectures:/) {
        sub(/[[:space:]]*$/, "", block)
        block = block "\nArchitectures: " arch
      }
      print block
    }
  ' "${src}" >"${tmp}"
  # Drop a trailing extra blank record awk ORS may leave.
  sed -i -e :a -e '/^\n*$/{$d;N;ba' -e '}' "${tmp}" 2>/dev/null || true
  if [[ ! -s "${tmp}" ]]; then
    rm -f "${tmp}"
    echo "amd64-qemu-userland: refused to empty ${src}" >&2
    exit 1
  fi
  mv "${tmp}" "${src}"
}

shopt -s nullglob
for src in /etc/apt/sources.list.d/*.sources; do
  flynn_pin_deb822_native_arch "${src}"
done
shopt -u nullglob

if [[ -f /etc/apt/sources.list ]]; then
  sed -i -E '/^deb \[arch=/! s|^deb |deb [arch='"${native_arch}"'] |' /etc/apt/sources.list
fi

cat >/etc/apt/sources.list.d/ubuntu-amd64.sources <<EOF
Types: deb
URIs: http://mirrors.edge.kernel.org/ubuntu
Suites: noble noble-updates noble-backports
Components: main universe
Architectures: amd64
Signed-By: ${keyring}

Types: deb
URIs: http://security.ubuntu.com/ubuntu
Suites: noble-security
Components: main universe
Architectures: amd64
Signed-By: ${keyring}
EOF

dpkg --add-architecture amd64
apt-get update --error-on=any
apt-get install -y --no-install-recommends \
  libc6:amd64 \
  libstdc++6:amd64 \
  libgcc-s1:amd64 \
  libatomic1:amd64 \
  zlib1g:amd64 \
  libssl3:amd64 \
  libsqlite3-0:amd64 \
  libsqlite3-dev:amd64

if [[ ! -e /lib64/ld-linux-x86-64.so.2 ]] && [[ ! -e /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 ]]; then
  echo "amd64-qemu-userland: amd64 dynamic linker missing after install" >&2
  dpkg -L libc6:amd64 | head >&2 || true
  exit 1
fi

echo "amd64-qemu-userland: installed amd64 glibc userland for qemu-user"

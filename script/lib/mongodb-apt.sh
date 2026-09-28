#!/bin/bash
# MongoDB 8.0 apt source for Ubuntu 24.04 (noble).
#
# InRelease is signed by 41DE058A4E7DCA05 (MongoDB 8.0 Release Signing Key).
# Apt on noble accepts an ASCII-armored key via signed-by=. Do not convert
# the key with gpg dearmor: noninteractive Vagrant provision has no /dev/tty,
# that conversion fails, and apt-get update then reports NO_PUBKEY.

FLYNN_MONGODB_APT_KEY_URL="https://pgp.mongodb.com/server-8.0.asc"
FLYNN_MONGODB_APT_KEYRING="/etc/apt/keyrings/mongodb-server-8.0.asc"
FLYNN_MONGODB_APT_LIST="/etc/apt/sources.list.d/mongodb-org-8.0.list"
FLYNN_MONGODB_APT_KEY_ID="41DE058A4E7DCA05"

flynn_mongodb_install_apt_repo() {
  install -m 0755 -d /etc/apt/keyrings
  local tmp
  tmp="$(mktemp)"
  if ! curl -fsSL --retry 5 --retry-delay 2 \
    "${FLYNN_MONGODB_APT_KEY_URL}" -o "${tmp}"; then
    echo "failed to download MongoDB apt signing key from ${FLYNN_MONGODB_APT_KEY_URL}" >&2
    rm -f "${tmp}"
    return 1
  fi
  if ! grep -q "BEGIN PGP PUBLIC KEY BLOCK" "${tmp}"; then
    echo "MongoDB apt key fetch did not return a PGP public key" >&2
    rm -f "${tmp}"
    return 1
  fi
  # Issuer packet for 41DE058A4E7DCA05 is encoded in the armored key as Qd4Fik59ygU.
  if ! grep -q 'Qd4Fik59ygU' "${tmp}"; then
    echo "MongoDB apt key is missing InRelease signer 41DE058A4E7DCA05" >&2
    rm -f "${tmp}"
    return 1
  fi
  install -m 0644 "${tmp}" "${FLYNN_MONGODB_APT_KEYRING}"
  rm -f "${tmp}"
  local codename
  codename="$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")"
  echo "deb [ arch=amd64,arm64 signed-by=${FLYNN_MONGODB_APT_KEYRING} ] https://repo.mongodb.org/apt/ubuntu ${codename}/mongodb-org/8.0 multiverse" \
    > "${FLYNN_MONGODB_APT_LIST}"
}

#!/bin/bash
# The builder box can ship a hollow /usr/local/go: thousands of zero-byte
# files that are still executable. `[[ -x go ]]` then passes and `go build`
# exits 0 without writing a binary. The same box leaves the snakeoil cert
# empty, so PostgreSQL refuses to start. Repair both before the Linux gate.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"

go_ok=0
if [[ -s /usr/local/go/bin/go && -x /usr/local/go/bin/go ]]; then
  if /usr/local/go/bin/go version >/dev/null 2>&1; then
    go_ok=1
  fi
fi
if [[ "${go_ok}" -ne 1 ]]; then
  echo "builder Go toolchain is missing or empty; installing pinned Go"
  bash "${FLYNN_VAGRANT_GUEST}/ensure-go.sh"
fi
if ! /usr/local/go/bin/go version >/dev/null 2>&1; then
  echo "builder Go toolchain still does not run after repair" >&2
  exit 1
fi

pem="/etc/ssl/certs/ssl-cert-snakeoil.pem"
key="/etc/ssl/private/ssl-cert-snakeoil.key"
if [[ ! -s "${pem}" || ! -s "${key}" ]]; then
  echo "regenerating empty snakeoil certificate so PostgreSQL can start"
  export DEBIAN_FRONTEND=noninteractive
  if command -v make-ssl-cert >/dev/null 2>&1; then
    make-ssl-cert generate-default-snakeoil --force-overwrite
  else
    install -d -m 710 /etc/ssl/private
    openssl req -new -x509 -days 3650 -nodes -subj "/CN=builder" \
      -keyout "${key}" -out "${pem}"
    chmod 640 "${key}"
  fi
fi
if [[ ! -s "${pem}" || ! -s "${key}" ]]; then
  echo "snakeoil certificate is still empty after repair" >&2
  exit 1
fi
echo "builder gate toolchain ready: $(/usr/local/go/bin/go version)"

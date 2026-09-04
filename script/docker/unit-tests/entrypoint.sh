#!/bin/bash
# Runs inside the flynn-unit-tests container. Starts local services and
# executes the unit test suite against the mounted source tree.
set -eo pipefail

export PATH="/usr/local/go/bin:${PATH}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"
export PGHOST="${PGHOST:-/var/run/postgresql}"
export PGSSLMODE="${PGSSLMODE:-disable}"
export FLYNN_TEST_IN_CONTAINER=1

cd /src

echo "==> Go $(go version)"

echo "==> Starting PostgreSQL"
pg_version="$(ls /usr/lib/postgresql | sort -V | tail -n1)"
pg_ctlcluster "${pg_version}" main start || service postgresql start
# Peer auth: create roles matching OS users used by tests.
sudo -u postgres createuser -s root 2>/dev/null || true

echo "==> Starting MariaDB"
service mariadb start 2>/dev/null || service mysql start 2>/dev/null || true

echo "==> Starting Redis"
service redis-server start 2>/dev/null || true

if [[ "${FLYNN_TEST_SKIP_CHECKS:-}" != "1" ]]; then
  echo "==> gofmt check"
  if ! util/commit-validator/validate-gofmt; then
    if [[ "${FLYNN_TEST_STRICT_CHECKS:-}" == "1" ]]; then
      exit 1
    fi
    echo "==> gofmt issues found (continuing; set FLYNN_TEST_STRICT_CHECKS=1 to fail)"
  fi
  echo "==> bats script tests"
  bats script/test
fi

echo "==> Building Flynn binaries (force rebuild for Linux container)"
# Host-mounted build/ may contain macOS Mach-O binaries; always rebuild.
./script/build-flynn -f -x "${FLYNN_VERSION:-dev}"

export GOROOT
GOROOT="$(readlink -f build/_go)"
export PATH="${PWD}/build/bin:${PATH}"

# Lower default parallelism inside Docker Desktop's smaller VM.
# shellcheck disable=SC2206
TEST_FLAGS=(${FLYNN_GO_TEST_FLAGS:--race -cover -p 2})

packages=()
if [[ $# -gt 0 ]]; then
  echo "==> Using package args: $*"
  packages=("$@")
elif [[ "${FLYNN_SKIP_VOLUME_TESTS:-}" == "1" ]]; then
  echo "==> Skipping host/volume tests (FLYNN_SKIP_VOLUME_TESTS=1)"
  mapfile -t packages < <(go list ./... | grep -v '/host/volume')
elif modprobe zfs 2>/dev/null && [[ -e /dev/zfs ]] && command -v zpool >/dev/null 2>&1; then
  echo "==> ZFS available; including host/volume tests"
  mapfile -t packages < <(go list ./...)
else
  # Docker Desktop's Linux VM usually lacks the ZFS kernel module.
  echo "==> ZFS kernel module unavailable in this Docker VM; skipping host/volume tests"
  echo "    (full volume coverage still runs on Linux CI / Vagrant)"
  mapfile -t packages < <(go list ./... | grep -v '/host/volume')
fi

echo "==> Running unit tests (${#packages[@]} packages)"
env GOROOT="${GOROOT}" GOFLAGS=-gcflags=all=-d=checkptr=0 \
  go test "${TEST_FLAGS[@]}" "${packages[@]}"

echo "==> Unit tests passed"

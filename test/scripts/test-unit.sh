#!/bin/bash
set -exo pipefail

util/commit-validator/validate-dco

util/commit-validator/validate-gofmt

bats script/test

# PostgreSQL for package unit tests (controller, blobstore, ...).
# Uses libpq-style variables (see pgx.ParseEnvLibpq): PGHOST, PGPORT, PGUSER,
# PGPASSWORD, PGSSLMODE, PGTEST_ADMIN_DATABASE.
#
# Start distro postgresql when we are using (or will use) the default socket path.
if [[ -z "${FLYNN_SKIP_PG_SERVICE:-}" ]] && [[ -z "${PGHOST:-}" || "${PGHOST}" == /var/run/postgresql ]] && command -v service >/dev/null 2>&1; then
  sudo service postgresql start || true
  stop_distro_pg() { sudo service postgresql stop || true; }
  trap stop_distro_pg EXIT
fi

# After start, pin PGHOST to the socket when unset and the directory exists.
if [[ -z "${PGHOST:-}" && -d /var/run/postgresql ]]; then
  export PGHOST=/var/run/postgresql
fi

make test-unit-root

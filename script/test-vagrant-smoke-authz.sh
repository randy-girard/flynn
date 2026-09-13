#!/bin/bash
# Regression: platform DBs (controller/blobstore/…) require the cluster key.
# User-app roles can CONNECT only to their own database (PUBLIC CONNECT revoked).
# Host unit gate must run the Darwin-safe authz / postgres-api / httphelper tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
authz="${ROOT}/controller/authz/http_test.go"
pgapi="${ROOT}/appliance/postgresql/cmd/flynn-postgres-api/main_test.go"
httphelper="${ROOT}/pkg/httphelper/response_writer_test.go"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${smoke}" './controller/authz/' \
  "host unit gate must run controller/authz (platform app HTTPDenied)"
need "${smoke}" './appliance/postgresql/cmd/flynn-postgres-api/' \
  "host unit gate must run postgres-api quoteIdent/REVOKE CONNECT tests"
need "${smoke}" './pkg/httphelper/' \
  "host unit gate must run httphelper SetContext (token stashed for appLookup)"
need "${authz}" 'TestHTTPAllowed' \
  "authz must keep HTTPAllowed table tests"
need "${authz}" 'app_write_cannot_psql_controller' \
  "authz must deny flynn run / pg psql on controller for app-scoped tokens"
need "${authz}" 'app_write_cannot_psql_blobstore' \
  "authz must deny flynn run / pg psql on blobstore for app-scoped tokens"
need "${authz}" 'app_write_cannot_psql_router' \
  "authz must deny flynn run / pg psql on router for app-scoped tokens"
need "${authz}" 'cluster_key_can_psql_controller' \
  "cluster key must still open controller jobs (pg psql)"
need "${authz}" 'TestSystemAppAllowed' \
  "appLookup UUID path must keep SystemAppAllowed tests"
need "${authz}" 'TestTokenFromContext' \
  "muxHandler must stash the token so appLookup can see it"
need "${pgapi}" 'TestQuoteIdent' \
  "postgres-api must quote database names for REVOKE CONNECT"
need "${pgapi}" 'TestRevokeConnectSQL' \
  "postgres-api must emit REVOKE CONNECT ON DATABASE … FROM PUBLIC"
need "${httphelper}" 'TestSetContext' \
  "ResponseWriter.SetContext must be covered (controller stores the token there)"
need "${smoke}" 'cli-pg-connect' \
  "smoke must prove the user-app role can CONNECT only to its own database"
need "${smoke}" 'has_database_privilege' \
  "CONNECT isolation must use has_database_privilege (not a TCP probe)"
need "${smoke}" "THEN 't' ELSE 'f'" \
  "CONNECT probe must emit t/f (boolean||boolean prints true/false and failed 2026-09-13)"
need "${smoke}" 'smoke_pg_tf' \
  "CONNECT probe must normalize true/false to t/f"
need "${smoke}" 'cli-pg-no-controller' \
  "smoke must prove the user-app role cannot CONNECT to the controller database"
need "${smoke}" 'cli-pg-controller' \
  "smoke must flynn -a controller pg psql with the cluster key"
need "${smoke}" 'cli-pg-blobstore' \
  "smoke must flynn -a blobstore pg psql with the cluster key"
need "${smoke}" 'flynn1 -a controller pg psql' \
  "platform console probe must use the cluster CLI (not a user job)"
need "${ROOT}/script/test-vagrant-smoke-backup.sh" 'grep -q' \
  "backup SIGPIPE contract must keep forbidding tar -tf | grep -q"
need "${ROOT}/script/test-vagrant-smoke-data-and-passes.sh" 'ui_table_cell' \
  "report alignment contract must keep ui_table_cell width checks"

echo "ok platform DB consoles are unit-tested and smoked (cluster key vs user CONNECT)"

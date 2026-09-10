#!/bin/bash
# Regression: smoke must seed every datastore, deploy test/apps/upgrade-smoke,
# run two --force upgrades, and print a per-engine persistence report.
# Sirenia/redis volume bugs have shown up on the second pass after the first
# succeeded (seen 2026-09-09 mariadb hang / redis empty GET).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
app="${ROOT}/test/apps/upgrade-smoke"

need() {
  local needle=$1 msg=$2
  if ! grep -qE "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_file "${app}/main.go" "smoke app must live in test/apps/upgrade-smoke"
need_file "${app}/go.mod" "upgrade-smoke must be a Go module (git-push buildpack)"
need_file "${app}/Procfile" "upgrade-smoke must have a Procfile"
need_file "${app}/data/seed.txt" "upgrade-smoke must embed at least one data file"
grep -q 'go:embed data' "${app}/main.go" || { echo "upgrade-smoke must embed data/ blobs" >&2; exit 1; }
grep -q '/status' "${app}/main.go" || { echo "upgrade-smoke must serve GET /status" >&2; exit 1; }

need 'test/apps/upgrade-smoke' \
  "smoke must deploy test/apps/upgrade-smoke (not mutate test/apps/http)"
need 'postgres mysql mongodb redis kafka clickhouse' \
  "smoke must provision every datastore provider"
need 'DATASTORE_PROVIDERS' \
  "smoke must iterate a shared provider list"
need 'SMOKE_SEED_ROWS' \
  "smoke must seed a configurable number of dummy rows/keys"
need 'SMOKE_BLOB_COUNT' \
  "smoke must embed a configurable number of slug blobs"
need 'generate_series' \
  "smoke must bulk-insert postgres dummy rows"
need 'smoke_payload' \
  "smoke must seed a 1KB payload table (postgres/mysql) so restarts copy real data"
need 'payload TEXT' \
  "mysql payload column must not be named blob (BLOB is reserved in MariaDB)"
need 'dummy:' \
  "smoke must SET redis dummy keys (not only smoke_probe)"
need 'kafka topics create smoke_probe' \
  "smoke must create a kafka topic whose metadata lives on /data"
need 'CREATE DATABASE IF NOT EXISTS smoke_db ENGINE = Atomic' \
  "clickhouse seed must create the DB without ON CLUSTER (DistributedDDL may be absent)"
need 'CHECK_FILE' \
  "per-engine checks must be written to a file (run_step is a subshell)"
need 'smoke_db.rows' \
  "smoke must insert clickhouse dummy rows"
need 'UPGRADE_PASSES' \
  "smoke must run more than one --force tarball update"
need 'post-upgrade-2' \
  "smoke must assert pass-1 markers still exist after the second upgrade"
need 'sirenia_primary_read_write' \
  "smoke must wait for postgres/mariadb/mongodb after each upgrade pass"
need 'wait_datastores_ready "after bootstrap" postgres' \
  "bootstrap must only wait for postgres (mariadb/mongodb stay scaled to 0 until resource add)"
need 'wait_datastores_ready "after resource add" postgres mariadb mongodb redis' \
  "after provisioning, smoke must wait for every scaled sirenia appliance plus redis"
need 'wait_datastores_ready "after upgrade' \
  "after each --force update, smoke must wait for postgres/mariadb/mongodb/redis"
need 'record_check' \
  "smoke must record per-engine results for the final report"
need 'print_datastore_report' \
  "smoke must print an App & datastore persistence table"
need 'assert_app_status' \
  "smoke must verify /status resource env after each upgrade"
need 'probe_app_http' \
  "HTTP wait retries must not record FAIL/PASS on every attempt"

if ! grep -F 'seq 1 "${UPGRADE_PASSES}"' "${smoke}" >/dev/null; then
  echo "smoke must loop upgrade passes with seq 1 UPGRADE_PASSES" >&2
  exit 1
fi

echo "ok smoke seeds all datastores, deploys test/apps/upgrade-smoke, and reports per engine"

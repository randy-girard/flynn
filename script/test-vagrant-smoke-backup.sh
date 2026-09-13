#!/bin/bash
# Regression: after upgrades, smoke must take a flynn cluster backup, wipe
# Flynn with --clean, bootstrap --from-backup, and prove postgres/mysql/mongodb
# data survived. Redis/Kafka/ClickHouse volumes are not in the cluster backup.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

need() {
  local needle=$1 msg=$2
  if ! grep -qE -e "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need 'SKIP_BACKUP' \
  "smoke must allow skipping cluster backup/restore"
need 'flynn cluster backup --file' \
  "smoke must take a full-cluster backup via the CLI"
need '/tmp/flynn-smoke-backup.tar' \
  "backup copy must live in /tmp so install --clean cannot delete it"
need 'backup_restore_path' \
  "smoke must keep a helper for the on-node restore path"
need 'step_cluster_backup' \
  "smoke must have a dedicated cluster backup step"
need 'step_bootstrap_from_backup' \
  "smoke must bootstrap with --from-backup after wiping Flynn"
need '--from-backup' \
  "restore must call flynn-host bootstrap --from-backup"
need 'step_verify_after_restore' \
  "smoke must re-verify apps and datastores after restore"
need 'assert_restored_datastores' \
  "restore verify must not reuse assert_databases (redis/kafka/clickhouse data is not in the backup)"
need 'wait_datastores_ready "after restore" postgres mariadb mongodb redis kafka clickhouse-ping' \
  "after restore, wait for SQL appliances plus empty redis/kafka/clickhouse engines"
need 'mysql.sql.gz' \
  "cluster backup must include the MariaDB dump (smoke always provisions mysql)"
need 'mongodb.archive.gz' \
  "cluster backup must include the MongoDB dump (smoke always provisions mongodb)"
need 'postgres.sql.gz' \
  "cluster backup must include postgres.sql.gz"
need 'flynn.json' \
  "cluster backup must contain flynn.json"
need 'keys not in cluster backup' \
  "redis after restore must PING only; keys are not in the cluster backup"
need 'topic data not in cluster backup' \
  "kafka after restore must only require the topics CLI"
need 'rows not in cluster backup' \
  "clickhouse after restore must only require SELECT 1"
need 'clickhouse-ping' \
  "restore must not wait on clickhouse_seed_ready (smoke_db is not restored)"
need 'kafka_is_ready' \
  "smoke must wait for kafka topics CLI after restore"
need 'post-restore' \
  "restore checks must be recorded as a post-restore phase"
need 'Reinstall for restore' \
  "restore must reinstall Flynn (--clean) before bootstrap --from-backup"
need 'Init layer-0 for restore' \
  "restore must re-init peer-ips after --clean"

if grep -q 'assert_databases post-restore' "${smoke}"; then
  echo "post-restore must not call assert_databases (redis/kafka/clickhouse would FAIL)" >&2
  exit 1
fi

echo "ok smoke takes a cluster backup and restores with --from-backup"

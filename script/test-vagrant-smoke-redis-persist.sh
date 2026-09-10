#!/bin/bash
# Regression: redis appliance data must survive flynn-host update.
# 2026-09-09: volume reuse succeeded but GET smoke_probe was empty because
# redis-server was SIGKILL'd without AOF/SHUTDOWN SAVE.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

if ! grep -q 'aof_enabled:1' "${smoke}"; then
  echo "smoke script must assert redis AOF is enabled (RDB save 900 1 does not flush before upgrade)" >&2
  exit 1
fi
if ! grep -q 'GET smoke_probe' "${smoke}"; then
  echo "smoke script must GET smoke_probe after upgrade" >&2
  exit 1
fi

proc="${ROOT}/appliance/redis/process.go"
if ! grep -q 'appendonly yes' "${proc}"; then
  echo "redis.conf must enable AOF so SETs survive a non-graceful kill" >&2
  exit 1
fi
if ! grep -q 'SHUTDOWN", "SAVE"' "${proc}" && ! grep -q 'SHUTDOWN SAVE' "${proc}"; then
  echo "Process.Stop must issue SHUTDOWN SAVE before signalling redis-server" >&2
  exit 1
fi

main="${ROOT}/appliance/redis/cmd/flynn-redis/main.go"
if ! grep -q 'BeforeExit(func() { m.Close() })' "${main}"; then
  echo "flynn-redis must Close (stop redis-server) on SIGTERM, not only the heartbeater" >&2
  exit 1
fi

echo "ok redis upgrade persistence (AOF + SHUTDOWN SAVE + SIGTERM Close)"

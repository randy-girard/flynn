#!/bin/bash
# Regression: after jobs are running (and after flynn-host update), hosts must
# subscribe to cgroup v2 OOM events. The old runc NotifyOOM looks for
# memory.oom_control, which does not exist on the unified hierarchy.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
oom="${ROOT}/host/oom.go"
backend="${ROOT}/host/libcontainer_backend.go"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${oom}" 'memory.events' "host OOM watcher must read cgroup v2 memory.events"
need "${oom}" 'oom_kill' "host OOM watcher must watch the oom_kill counter"
need "${ROOT}/host/oom_test.go" 'TestWatchMemoryEvents' "OOM watcher must have unit tests"
need "${backend}" 'subscribeOOM' "libcontainer backend must use subscribeOOM, not only NotifyOOM"
need "${smoke}" 'assert_oom_subscription' "upgrade smoke must assert OOM subscription"
need "${smoke}" 'unable to subscribe to OOM notifications' "smoke must fail if OOM subscribe warnings reappear"
need "${smoke}" 'user system background' \
  "OOM check must look at every partition, not only user jobs (3-node peers can have an empty user/ dir)"
if grep -qE 'no memory.events under /sys/fs/cgroup/flynn/user' "${smoke}"; then
  echo "OOM check must not fail a host that has no user jobs but has system cgroups" >&2
  exit 1
fi

echo "ok OOM cgroup v2 watch is covered by unit code and upgrade smoke"

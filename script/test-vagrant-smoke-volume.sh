#!/bin/bash
# Regression: smoke must create a persistent volume, write a file, read it
# back after a job restart, then decommission/delete the volume.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

need() {
  local needle=$1 msg=$2
  if ! grep -qE "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need 'step_volume' \
  "smoke must have a persistent-volume lifecycle step"
need 'VOL_SMOKE_WROTE' \
  "volume step must write a unique token into /data"
need 'VOL_SMOKE_READ' \
  "volume step must read the token back after restarting the vol job"
need 'volume decommission' \
  "volume step must decommission the volume through the Flynn CLI"
need 'flynn-host volume:delete' \
  "volume step must destroy the volume on the host after decommission"
need 'scale vol=1' \
  "volume step must scale a process type that has a persistent /data volume"
need 'scale vol=0' \
  "volume step must stop the vol job so the scheduler can reattach the same volume"
need 'volume detached' \
  "volume step must wait for the vol job to stop before scaling back up"
need 'volume unattached' \
  "volume step must wait until flynn volume show JobID is empty before scale-up"
need 'vol_scale_zero' \
  "volume cleanup must scale vol=0 before decommission"
need 'vol_unattached' \
  "volume step must treat a missing holder as free, not only flynn ps"
need '"path": "/data"' \
  "volume process type must request a persistent /data volume (not delete_on_stop)"
need '/tmp/vol-release-' \
  "volume release JSON must be written on the VM, not via a lagged Vagrant share"
need 'python3 -' \
  "volume job command must be JSON-encoded so \$(cat) is not expanded on the host"
need 'maybe_run_cli_and_volume' \
  "CLI skip flags must also skip the volume probe"
need 'SKIP_VOLUME' \
  "smoke must allow skipping only the volume probe"
need 'Persistent volume \(pre-upgrade\)' \
  "smoke must run the volume lifecycle before the first --force update"
need 'Persistent volume after add-node' \
  "add topology must re-run the volume lifecycle after joining a host"
need 'Persistent volume after remove-node' \
  "remove topology must re-run the volume lifecycle after draining a host"
need 'Persistent volume after upgrade' \
  "smoke must re-run the volume lifecycle after flynn-host update"
need 'Persistent volume after restore' \
  "smoke must re-run the volume lifecycle after backup restore"
if grep -A20 'vol": {' "${smoke}" | grep -q 'delete_on_stop'; then
  echo "volume smoke must persist /data across job stop (no delete_on_stop)" >&2
  exit 1
fi

echo "ok persistent volume write/read/delete smoke"

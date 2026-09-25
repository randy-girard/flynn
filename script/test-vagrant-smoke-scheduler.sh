#!/bin/bash
# Regression: smoke must schedule an interval job on an uploaded app after
# the scheduler plugin is installed and prove the runner actually fires.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${ROOT}/pkg/plugin/official-plugins.json" '"kind": "scheduler"' \
  "official catalog must include the scheduler plugin"
need "${ROOT}/controller/types/types.go" 'EventTypeScheduler' \
  "controller events must include scheduler fires"
need "${ROOT}/cli/plugin_cmd.go" 'PartitionTypeSystem' \
  "plugin CLI jobs must run in the system partition so scheduler.discoverd resolves"
need "${smoke}" 'PLUGIN_SMOKE_APPS:-redis mysql mongodb kafka clickhouse dashboard www discovery otel scheduler pipeline' \
  "default plugin install list must include scheduler"
need "${smoke}" 'scheduler_smoke_wanted' \
  "upgrade smoke must gate scheduler probes on PLUGIN_SMOKE_APPS"
need "${smoke}" 'probe_scheduler_interval_job' \
  "upgrade smoke must schedule an interval job on an uploaded app"
need "${smoke}" 'scheduler add --command "echo scheduler-smoke"' \
  "scheduler smoke must add a job against one of the uploaded apps"
need "${smoke}" '--every 10s' \
  "scheduler smoke must use a short interval so the runner can fire in-band"
need "${smoke}" '--type web' \
  "scheduler smoke must inherit mounts from the uploaded app web process"
need "${smoke}" 'scheduler_job_fired' \
  "scheduler smoke must wait until the runner records a fire"
need "${smoke}" 'last_run_at' \
  "scheduler smoke must require last_run_at after the interval fires"
need "${smoke}" 'last_job_id' \
  "scheduler smoke must require last_job_id after the runner starts a one-off"
need "${smoke}" 'cli-scheduler-run' \
  "CLI step must record the scheduler interval fire"
need "${smoke}" 'cli-scheduler-remove' \
  "scheduler smoke must delete the interval job so it does not keep firing"
need "${smoke}" 'cli-help-scheduler' \
  "CLI step must show scheduler in flynn help after plugin install"
need "${smoke}" 'help scheduler add' \
  "scheduler --every docs live under flynn help scheduler add, not parent help"

if awk '/^step_install_plugins\(/,/^}/' "${smoke}" | grep -q 'probe_scheduler_interval_job'; then
  echo "scheduler interval fire cannot run at plugin-install time; apps are not deployed yet" >&2
  exit 1
fi
if ! awk '/^step_cli_functions\(/,/^}/' "${smoke}" | grep -q 'probe_scheduler_interval_job'; then
  echo "scheduler interval fire must run in the live CLI step after apps are uploaded" >&2
  exit 1
fi

echo "ok scheduler plugin interval-job smoke is covered"

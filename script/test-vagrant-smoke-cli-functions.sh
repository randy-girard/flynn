#!/bin/bash
# Regression: smoke must run a live flynn / flynn-host CLI step after verify
# (and after each upgrade). Unit tests already cover ./cli (host gate) and
# ./cli + ./host/cli (builder). Without a live step, controller/scheduler
# drift after flynn-host update is only caught as a confusing DB/HTTP miss.
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

need 'step_cli_functions' \
  "smoke must have a dedicated live CLI function step"
need 'SKIP_CLI' \
  "smoke must allow skipping live CLI probes"
need 'cli_probe' \
  "live CLI checks must record per-command PASS/FAIL"
need 'flynn1 apps' \
  "CLI step must list apps through the controller"
need 'cli-ps' \
  "CLI step must list jobs"
need 'cli-scale' \
  "CLI step must read formation scale"
need 'env get FLYNN_POSTGRES' \
  "CLI step must read datastore env via flynn env get"
need 'cli-resource' \
  "CLI step must list app resources for every datastore provider"
need 'cli-route' \
  "CLI step must list routes"
need 'cli-release' \
  "CLI step must list releases"
need 'log -n 20' \
  "CLI step must read logaggregator output without --follow"
need 'cli_run_job' \
  "CLI step must share a time-bounded flynn run helper"
need 'echo smoke-cli' \
  "CLI step must run a one-off job (scheduler + slugrunner)"
need 'docker-cli-run' \
  "CLI step must flynn run against the Dockerfile/container-stack app"
need 'echo docker-cli' \
  "container flynn run must execute a command without /runner/init"
need 'cat /start.sh' \
  "container flynn run must read a file from the Docker image"
need 'web.discoverd:8080' \
  "container flynn run must HTTP-get the running app process via discoverd"
need 'docker-cli-ps' \
  "CLI step must list the Dockerfile app jobs"
need 'docker-cli-log' \
  "CLI step must read Dockerfile app logs"
need 'mongodb dump' \
  "CLI step must dump mongodb (slimmed mongodump tools)"
need 'blobstore.discoverd/.well-known/status' \
  "CLI step must reach blobstore health from a slugrunner job"
need 'flynn-host version' \
  "CLI step must run flynn-host version (stripped host binary)"
need 'pg_available_extensions' \
  "CLI/seed must verify postgres PostGIS/pgRouting/Timescale still ship"
need 'timeout 90 flynn' \
  "flynn run must be time-bounded so a hung scheduler cannot stall smoke"
need 'meta set' \
  "CLI step must write app metadata (controller mutation without a new release)"
need 'flynn-host list' \
  "CLI step must list cluster hosts"
need 'cli-host-ps' \
  "CLI step must list host jobs"
need 'CLI functions \(pre-upgrade\)' \
  "smoke must run CLI probes before the first --force update"
need 'CLI functions after upgrade' \
  "smoke must re-run CLI probes after each --force update"
if grep -qE 'scale web=' "${smoke}"; then
  echo "CLI step must not scale web (creates extra jobs and races HTTP verify)" >&2
  exit 1
fi
if grep -qE 'env set ' "${smoke}"; then
  echo "CLI step must not flynn env set (creates a release and restarts web)" >&2
  exit 1
fi

echo "ok CLI function smoke step (live flynn + flynn-host probes)"

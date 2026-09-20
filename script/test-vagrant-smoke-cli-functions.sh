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

need './pkg/cliutil/' \
  "host unit gate must compile docopt string/list helpers used by colon commands"
need './host/fixer/' \
  "host unit gate must compile interactive flynn-host fix"
need './host/logmux/' \
  "host unit gate must compile OTLP log/metrics sinks"
need 'step_cli_functions' \
  "smoke must have a dedicated live CLI function step"
need 'SKIP_CLI' \
  "smoke must allow skipping live CLI probes"
need 'cli_probe' \
  "live CLI checks must record per-command PASS/FAIL"
need 'unknown_error|connection refused' \
  "cli_probe and cli_run_job must retry Flynn unknown_error from plugin dump/topics jobs"
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
if ! awk '/^cli_run_job\(/,/^}/' "${smoke}" | grep -q '2>&1'; then
  echo "cli_run_job must capture ssh stderr so unknown_error retries" >&2
  exit 1
fi
need 'echo smoke-cli' \
  "CLI step must run a one-off job (scheduler + slugrunner)"
need 'buildpack-cli-run' \
  "CLI step must flynn run against the custom .buildpacks app"
need 'echo buildpack-cli' \
  "custom-buildpack flynn run must execute a command in slugrunner"
need 'cat .buildpack-stamp' \
  "CLI step must read the custom compile stamp from the slug"
need 'docker-cli-run' \
  "CLI step must flynn run against the Dockerfile/container-stack app"
need 'echo docker-cli' \
  "container flynn run must execute a command without /runner/init"
need 'docker-push-run' \
  "CLI step must flynn run against the flynn docker push app"
need 'echo docker-push-cli' \
  "docker-push flynn run must execute a command in the pre-built image"
need 'cat /start.sh' \
  "container flynn run must read a file from the Docker image"
need 'userns-uid-map' \
  "CLI step must prove user jobs run in a user namespace"
need 'userns-ok' \
  "user-ns flynn run must see container 0, host uid >= 1000000, and root-owned image files"
need 'cli_userns_host' \
  "CLI step must check the host uid_map of a running user job"
need 'userns-user-ok' \
  "host-side probe must confirm user jobs are remapped"
need 'userns-sys-ok' \
  "host-side probe must confirm postgres stays in the host user ns"
need 'flynn-controller.type' \
  "userns-host must pick the formation web job, not a just-exited flynn run"
need '4294967295' \
  "system jobs must keep the host identity uid_map"
need 'net-isolate-peer' \
  "CLI step must prove user jobs cannot reach other apps on the overlay"
need 'net-isolate-internal' \
  "CLI step must prove user jobs cannot resolve postgres.discoverd (internal name)"
need 'net-isolate-api' \
  "CLI step must prove user jobs cannot reach postgres-api"
need 'leader.postgres.discoverd' \
  "CLI step must prove user jobs can still reach the provisioned DATABASE_URL host"
need 'docker-cli-ps' \
  "CLI step must list the Dockerfile app jobs"
need 'docker-cli-ps" "web"' \
  "git-push Dockerfile CLI ps must match process type web"
need 'docker-cli-scale" "web=' \
  "git-push Dockerfile CLI scale must show web="
need '/\^\$\{node\}-' \
  "userns-host must probe /proc on the host that owns the job"
need 'print; exit' \
  "userns-host must not use head -1 under pipefail (SIGPIPE rc=141)"
need 'docker-cli-log' \
  "CLI step must read Dockerfile app logs"
need 'cli-help-mongodb' \
  "CLI step must show mongodb in flynn help after plugin install"
need 'cli-mongo-dump' \
  "CLI step must dump mongodb via the delegated plugin job"
need 'cli-help-kafka' \
  "CLI step must show kafka in flynn help after plugin install"
need 'cli-kafka-topics' \
  "CLI step must list kafka topics via the delegated plugin job"
need 'cli-help-clickhouse' \
  "CLI step must show clickhouse in flynn help after plugin install"
need 'cli-clickhouse-databases' \
  "CLI step must list clickhouse databases via the delegated plugin job"
need 'cli-help-redis' \
  "CLI step must show redis in flynn help after plugin install"
need 'cli-help-mysql' \
  "CLI step must show mysql in flynn help after plugin install"
need 'cli-mysql-dump' \
  "CLI step must dump mysql via the delegated plugin job"
need 'cli-help-redis-doc' \
  "CLI step must fetch redis usage (redis-cli) from the plugin catalog"
need 'cli-redis-dump' \
  "CLI step must dump redis via the delegated plugin job"
need 'cli-redis-restore' \
  "CLI step must restore a redis dump via the delegated plugin job"
need 'probe_delegated_plugin_cli_hidden' \
  "plugin install must prove flynn help hides redis until the plugin is installed"
need 'plugin_has_delegated_cli' \
  "smoke must detect plugin CLI doc+actions from flynn-plugin.json"
need 'blobstore.discoverd/.well-known/status' \
  "CLI step must reach blobstore health from a system job (not a user slug)"
need 'flynn-host version' \
  "CLI step must run flynn-host version (stripped host binary)"
need 'pg_available_extensions' \
  "CLI/seed must verify postgres PostGIS/pgRouting/Timescale still ship"
need 'cli_pg_query' \
  "pg:psql probes must retry unknown_error after flynn-host upgrades"
need 'pg:psql' \
  "CLI postgres probes must use the colon form (space form prints an alias notice)"
if grep -qE '(^|[^:])pg psql' "${smoke}"; then
  echo "CLI step must not use space-form pg psql (use pg:psql)" >&2
  exit 1
fi
need 'FLYNN_SKIP_UPDATE_CHECK' \
  "smoke must skip GitHub upgrade notices so pg CONNECT matches stay exact"
need 'smoke_pg_tf' \
  "CLI pg probes must normalize boolean output (and ignore CLI notices)"

# smoke_pg_tf must keep the SQL token when flynn prints an upgrade notice.
eval "$(sed -n '/^smoke_pg_tf()/,/^}/p' "${smoke}")"
got="$(smoke_pg_tf $'A newer Flynn CLI is available (v20260917.2; this is v20260917.1). Run `flynn update` to upgrade.\nt,f,f\n')"
if [[ "${got}" != "t,f,f" ]]; then
  echo "smoke_pg_tf must extract t,f,f from CLI upgrade notice, got ${got}" >&2
  exit 1
fi
got="$(smoke_pg_tf $'A newer Flynn CLI is available (v20260917.2; this is v20260917.1). Run `flynn update` to upgrade.\nf\n')"
if [[ "${got}" != "f" ]]; then
  echo "smoke_pg_tf must extract trailing f from CLI upgrade notice, got ${got}" >&2
  exit 1
fi
got="$(smoke_pg_tf "t,f,f")"
if [[ "${got}" != "t,f,f" ]]; then
  echo "smoke_pg_tf must keep a bare t,f,f, got ${got}" >&2
  exit 1
fi
need 'cli-pg-connect' \
  "CLI step must prove the user-app role cannot CONNECT to postgres/template1"
need 'cli-pg-controller' \
  "CLI step must open controller psql with the cluster key"
need 'cli-pg-blobstore' \
  "CLI step must open blobstore psql with the cluster key"
need 'timeout 90 flynn' \
  "flynn run must be time-bounded so a hung scheduler cannot stall smoke"
need 'meta set' \
  "CLI step must write app metadata (controller mutation without a new release)"
need 'flynn-host list' \
  "CLI step must list cluster hosts"
need 'cli-host-ps' \
  "CLI step must list host jobs"
need 'cli-host-otel' \
  "CLI step must list OpenTelemetry exporters (otel plugin)"
need '14318' \
  "CLI step must show the dummy OTLP collector endpoint after otel install"
need 'cli-host-domain' \
  "CLI step must show cluster apex domain"
need 'cli-host-fix-help' \
  "CLI step must show interactive flynn-host fix --yes"
need 'grep -qE --' \
  "cli_probe must pass -- so patterns like --yes are not grep flags"
need 'installing Flynn CLI on node1' \
  "smoke must (re)install the synced CLI so SKIP_BUILD still picks up CLI fixes"
need 'CLI functions \(pre-upgrade\)' \
  "smoke must run CLI probes before the first --force update"
need 'CLI functions after upgrade' \
  "smoke must re-run CLI probes after each --force update"
if awk '/^step_cli_functions\(/,/^}/' "${smoke}" | grep -qE 'scale web='; then
  echo "CLI step must not scale web (creates extra jobs and races HTTP verify)" >&2
  exit 1
fi
if grep -qE 'env set ' "${smoke}"; then
  echo "CLI step must not flynn env set (creates a release and restarts web)" >&2
  exit 1
fi

echo "ok CLI function smoke step (live flynn + flynn-host probes)"

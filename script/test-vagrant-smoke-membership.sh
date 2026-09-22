#!/bin/bash
# Regression: smoke can join a node after the cluster is stable and then
# upgrade, and can drain a node while HTTP/DBs/deploys keep working.
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

need 'step_add_cluster_node' \
  "smoke must add a VM to an already-bootstrapped cluster"
need 'flynn-host init --peer-ips' \
  "joining a host must use flynn-host init --peer-ips of the running cluster"
need 'seed_joining_host_secrets' \
  "joining a host must copy DISCOVERD_AUTH_KEY from node1 before flynn-host starts"
need 'step_remove_cluster_node' \
  "smoke must remove a host from a running cluster"
need 'restore_drained_inventory' \
  "backup restore after drain must reinstall the original VMs so min-hosts can come online"
need 'flynn-host demote' \
  "removing a Raft peer must demote it before flynn-host is stopped"
need 'drain_host_jobs' \
  "remove-node must stop jobs via the host API; systemd stop leaves containers running"
need 'kill_leftover_containers' \
  "remove-node must SIGKILL leftover containerinit after flynn-host exits"
need '127.0.0.1:1113' \
  "job drain must talk to the departing host's local API while flynn-host is up"
need '/host/jobs' \
  "job drain must DELETE /host/jobs on the departing host"
need 'systemctl stop flynn-host.service' \
  "removed host must stop flynn-host after its jobs are drained"
need 'discoverd_host_jobs_gone' \
  "remove-node must wait until discoverd drops the drained host's instances"
need 'sirenia_primary_not_on_host' \
  "remove-node must wait for postgres/mariadb/mongodb primaries to leave the drained host"
need 'step_verify_membership' \
  "membership changes must re-verify HTTP, status, docker, and datastores"
need 'step_membership_deploy' \
  "membership changes must re-run slug git-push, Dockerfile git-push, docker push, and flynn run"
need 'slug app git dir missing' \
  "membership must git-push the regular slug app, not only the Dockerfile app"
need 'buildpack app git dir missing' \
  "membership must git-push the custom .buildpacks app after add/remove"
need 'smoke_docker_push_image 0' \
  "membership must flynn docker push a pre-built image after add/remove"
need 'git push attempt' \
  "membership docker git-push must retry; scale can lag after a host drain"
need 'membership-ok' \
  "membership deploy must run a one-off job on the slug app"
need 'after-add' \
  "add topology must verify and run CLI after the new host joins"
need 'after-remove' \
  "remove topology must verify and run CLI after the host is drained"
need 'TOPOLOGY_ACTION=add' \
  "add topology must boot 3-node HA then join node4"
need 'TOPOLOGY_LABEL="3-node-add"' \
  "add-node report phases must be 3-node-add/"
need 'TOPOLOGY_LABEL="3-node-remove"' \
  "remove-node report phases must be 3-node-remove/"
need 'remember_teardown_node node4' \
  "add topology must record node4 for teardown in the parent shell (run_step is a subshell)"
need 'append_live_node node4' \
  "parent shell must keep node4 in NODES after add (CLI host-list / overlay)"
if ! grep -Fq 'drained-node' "${smoke}"; then
  echo "parent shell must drop whichever host was actually drained (redis volume is host-local)" >&2
  echo '  missing drained-node file (run_step is a subshell)' >&2
  exit 1
fi
need 'refusing to remove node1' \
  "remove-node must keep node1 (CLI and bootstrap)"
need 'redis_job_host_from_ps' \
  "remove-node must parse redis placement from flynn ps ID (nodeN-uuid), not NAME"
if ! grep -Fq 'split($NF' "${smoke}"; then
  echo "redis host parse must use the last column; NAME is redis.1234 and never matches node3" >&2
  echo '  missing split($NF) in smoke' >&2
  exit 1
fi

# CREATED is "8 minutes ago"; NAME redis.1138 has no hyphen. A $1 split
# always "finds" a host that is not a node, so drain of node3 kills redis.
got_host="$(printf '%s\n' \
  'NAME TYPE STATE CREATED ID' \
  'redis.1138 redis up 8 minutes ago node3-22980b90-5e70-471a-9c31-78c1aa871211' \
  | awk 'NR>1 && $2=="redis" && tolower($3) ~ /up|running/ {
    split($NF, a, "-")
    if (a[1] ~ /^node[0-9]+$/) { print a[1]; exit }
  }')"
if [[ "${got_host}" != "node3" ]]; then
  echo "redis_job_host_from_ps fixture must yield node3, got '${got_host}'" >&2
  exit 1
fi
wrong_host="$(printf '%s\n' \
  'NAME TYPE STATE CREATED ID' \
  'redis.1138 redis up 8 minutes ago node3-22980b90-5e70-471a-9c31-78c1aa871211' \
  | awk 'NR>1 && $2=="redis" && tolower($3) ~ /up|running/ {
    split($1, a, "-")
    print a[1]
    exit
  }')"
if [[ "${wrong_host}" == "node3" ]]; then
  echo "NAME-column parse must not look like a host id (got ${wrong_host})" >&2
  exit 1
fi
need 'clickhouse replica' \
  "remove-node clickhouse checks need every replica seeded (leader-only MergeTree dies with the drained host)"
need 'bounce_controller_scheduler' \
  "remove-node must restart the controller scheduler; drain can panic its loop"
need 'controller_scheduler_replaced' \
  "scheduler bounce must wait for a new job ID, not the draining one still marked up"
need 'controller_scheduler_live_line' \
  "scheduler bounce must ignore drained-host scheduler rows still marked up"
need 'sync_cluster_monitor_hosts' \
  "add/remove must update cluster-monitor hosts so flynn-host update does not wait for drained peers"
need 'Upgrade pass' \
  "add/remove topologies still run tarball upgrades after membership changes"

if grep -qE 'flynn -a "\$\{APP_NAME\}" env set' "${smoke}"; then
  echo "membership steps must not env-set the slug app (creates a release)" >&2
  exit 1
fi

echo "ok smoke add-node and remove-node membership topologies"

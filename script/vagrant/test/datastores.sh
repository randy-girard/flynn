#!/bin/bash
# Regression: --item datastores stands up the existing smoke cluster, installs
# every datastore plugin, and resource:adds each onto a throwaway app. It must
# not invent a second cluster harness or require the multi-hour singleton path.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
smoke="${ROOT}/script/vagrant/suite.sh"
entry="${ROOT}/script/vagrant-smoke.sh"
example="${ROOT}/smoke-matrix.example.yaml"
readme="${ROOT}/test/README.md"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -Fq -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing ${needle} in ${file}" >&2
    exit 1
  fi
}

need "${smoke}" 'step_provision_datastores' \
  "smoke must have a throwaway-app datastore provision step"
need "${smoke}" 'SMOKE_DATASTORE_PROVISION' \
  "the datastores item must gate throwaway provision without breaking RESUME_AT=upgrade SKIP_DEPLOY"
need "${smoke}" 'DATASTORE_SMOKE_APP' \
  "throwaway provision must use a dedicated app name"
need "${smoke}" 'smoke-datastores' \
  "default throwaway app must be smoke-datastores"
need "${smoke}" 'resource_add_candidates' \
  "mysql resource:add must try mysql then fall back to mariadb"
need "${smoke}" 'add_throwaway_resource' \
  "each engine must be resource:added on the throwaway app"
need "${smoke}" 'throwaway_resource_ok' \
  "provision must accept env URL or a listed resource"
need "${smoke}" 'wait_selected_datastores_ready "after throwaway resource add" 1' \
  "after throwaway resource:add, smoke must wait for selected engines including kafka/clickhouse pings"
need "${smoke}" 'seed_datastores' \
  "throwaway provision must seed each engine after resource:add"
need "${smoke}" 'exercise_datastore_followers' \
  "throwaway provision must create followers and verify seed on them"
need "${smoke}" 'teardown_throwaway_datastores' \
  "throwaway provision must resource:remove after seed and follow"
need "${smoke}" '--follow' \
  "replica engines must be provisioned with resource:add --follow"
need "${ROOT}/cli/resource.go" 'listRec(w, "NAME", "PROVIDER", "ID")' \
  "flynn resource must print NAME for pg:psql, --follow, and --join"
need "${smoke}" 'tolower($2)==tolower(p)' \
  "resource teardown must match PROVIDER (column 2) now that NAME is first"
need "${smoke}" 'datastore_extra_node_flag' \
  "kafka/mongodb must use --join; replica engines keep --follow"
need "${smoke}" 'mongodb:nodes:add' \
  "mongodb --join adds a replica-set member; nodes:add remains the fallback"
need "${example}" 'topologies: "1,3"' \
  "datastores must run 1-node and 3-node clusters"
need "${smoke}" 'tenant_mysql_ping' \
  "throwaway mysql must ping via mysql console, not only legacy mariadb sirenia"
need "${smoke}" 'tenant_mongodb_ping' \
  "throwaway mongodb must ping the isolated instance, not only leader.mongodb.discoverd"
need "${smoke}" 'MONGODB_REPLICA_SERVICE' \
  "mongodb replica wait must count discoverd peers on the tenant service, not the plugin app"
need "${smoke}" 'app_has_identity_env' \
  "wait_datastores_ready must use tenant CLI pings when FLYNN_POSTGRES/FLYNN_MYSQL is set"
need "${smoke}" 'Provision datastore plugins' \
  "the topology loop must run the throwaway provision step"
need "${smoke}" 'run_smoke_topologies' \
  "datastores must reuse the existing topology/cluster harness"
need "${smoke}" 'item_id}" == "datastores"' \
  "SMOKE_DATASTORE_PROVISION must turn on only for the datastores matrix item"
need "${entry}" '--item datastores' \
  "vagrant-smoke.sh must document --item datastores"
need "${example}" 'id: datastores' \
  "example matrix must include the datastores item"
need "${readme}" '--item datastores' \
  "test/README.md must document how to run the datastore plugin smoke"

if ! awk '/^resource_add_candidates\(/,/^}/' "${smoke}" | grep -q 'mysql'; then
  echo "resource_add_candidates must list mysql before mariadb" >&2
  exit 1
fi
if ! awk '/^resource_add_candidates\(/,/^}/' "${smoke}" | grep -q 'mariadb'; then
  echo "resource_add_candidates must fall back to mariadb" >&2
  exit 1
fi

# mysql then mariadb (not the other way when the requested name is mysql).
cands="$(awk '/^resource_add_candidates\(/,/^}/' "${smoke}")"
mysql_line="$(echo "${cands}" | grep -n 'printf.*mysql mariadb' || true)"
if [[ -z "${mysql_line}" ]]; then
  echo "mysql candidate order must be: mysql mariadb" >&2
  exit 1
fi

if awk '/^apply_resume_at_skips\(/,/^}/' "${smoke}" | grep -q 'SMOKE_DATASTORE_PROVISION'; then
  echo "RESUME_AT skips must not force throwaway provision (upgrade/backup set SKIP_DEPLOY=1)" >&2
  exit 1
fi

if awk '/^exercise_datastore_followers\(/,/^}/' "${smoke}" | grep -q 'APP_NAME="${fol}"'; then
  echo "follower ping must not flynn -a the database app (cannot run a job there)" >&2
  exit 1
fi
need "${smoke}" 'DATASTORE_RESOURCE_APP' \
  "follower verify must name the instance on the throwaway app (flynn pg:psql pg-xxx)"
need "${smoke}" 'flynn_ds' \
  "datastore CLI helper must insert the named resource before --"
need "${smoke}" 'attempt ${attempt}/20' \
  "resource:add must retry; 3-node controller db-runtimes are in-memory per web job"
need "${smoke}" 'kafka_cluster_ready' \
  "kafka --join must wait for a second broker in the same cluster"
need "${smoke}" 'redis_follower_seeded' \
  "redis follower verify must wait for replicaof to copy seeded keys, not only PING"

# skip_deploy is set so git-push is skipped, but throwaway provision still
# boots both 1-node and 3-node clusters. The single-topology SKIP_DEPLOY
# guard must not apply when SMOKE_DATASTORE_PROVISION=1.
if ! awk '/^parse_smoke_topologies\(/,/^}/' "${smoke}" | grep -q 'SMOKE_DATASTORE_PROVISION'; then
  echo "parse_smoke_topologies must allow SKIP_DEPLOY with topologies 1,3 when throwing away datastores" >&2
  exit 1
fi

if grep -q 'script/test-vagrant-smoke' "${smoke}" "${entry}"; then
  echo "datastores must use script/vagrant/, not restored deleted test-vagrant-smoke scripts" >&2
  exit 1
fi

echo "ok --item datastores provisions, seeds, follows, and tears down every datastore plugin on 1- and 3-node clusters"

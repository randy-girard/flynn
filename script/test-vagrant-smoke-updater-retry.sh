#!/bin/bash
# Regression: 3-node flynn-host update can fail postgres sirenia HA with
# "timed out waiting for new instance to come up" after host restarts.
# That string has no "sirenia" substring, so the updater must retry it as a
# scale/instance timeout and sirenia startInstance must poll discoverd.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
unsettled="${ROOT}/pkg/updaterdeploy/deploy_unsettled.go"
unsettled_test="${ROOT}/pkg/updaterdeploy/deploy_unsettled_test.go"
sirenia="${ROOT}/controller/worker/deployment/sirenia.go"
sirenia_health="${ROOT}/controller/worker/deployment/sirenia_health.go"
github_updater="${ROOT}/host/cli/github_updater.go"
incluster="${ROOT}/updater/updater.go"

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_in() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file "${unsettled}" "transient deploy retry matcher must exist"
need_file "${unsettled_test}" "transient deploy retry tests must exist"
need_file "${sirenia}" "sirenia deploy must exist"
need_file "${sirenia_health}" "sirenia discoverd poll helpers must exist"
need_file "${github_updater}" "GitHub updater must exist"
need_file "${incluster}" "in-cluster updater must exist"

need_in "${unsettled}" 'timed out waiting for new instance to come up' \
  "HA sirenia instance wait must be a retryable scale timeout"
need_in "${unsettled}" 'MaxTransientDeployAttempts' \
  "retry budget must depend on the error (long waits vs NXDOMAIN)"
need_in "${unsettled}" 'maxScaleTimeoutDeployAttempts' \
  "long instance waits must not reuse the 24-attempt NXDOMAIN budget"
need_in "${unsettled_test}" 'timed out waiting for new instance to come up' \
  "unit tests must cover the HA sirenia instance timeout"
need_in "${unsettled_test}" 'HA sirenia new-instance timeout must retry' \
  "retry budget tests must name the HA timeout case"
need_in "${sirenia_health}" 'lookupSireniaPeer' \
  "sirenia must poll discoverd for a new-release peer"
need_in "${sirenia_health}" 'excludeIDs' \
  "HA startInstance poll must skip already-started new peers"
need_in "${sirenia}" 'lookupSireniaPeer\(svc, d.NewReleaseID, processType, exclude' \
  "HA startInstance wait must poll discoverd excluding known new peers"
need_in "${github_updater}" 'MaxTransientDeployAttempts\(deployErr\)' \
  "flynn-host update must size retries from the deploy error"
need_in "${incluster}" 'MaxTransientDeployAttempts\(deployErr\)' \
  "in-cluster updater must size retries from the deploy error"
need_in "${ROOT}/controller/scheduler/scheduler.go" 'IsSireniaSingleton' \
  "HA rolling extra peers must not wait on busy singleton volumes"
need_in "${ROOT}/controller/scheduler/scheduler_test.go" 'TestShouldDeferVolumeAllocationHARollingNewPeer' \
  "scheduler tests must cover HA rolling new-peer volume allocation"
need_in "${ROOT}/controller/types/types.go" 'func \(r \*Release\) IsSireniaSingleton' \
  "Release must distinguish HA SINGLETON=false from rolling count=1"
need_in "${ROOT}/controller/types/types.go" 'func \(a \*Artifact\) IsSlugrunner' \
  "slugrunner-24 git apps must be recognized as slugrunner on update"
need_in "${ROOT}/host/cli/github_updater.go" 'artifact.IsSlugrunner\(\)' \
  "flynn-host update must redeploy heroku-24 slug apps"
need_in "${ROOT}/updater/updater.go" 'artifact.IsSlugrunner\(\)' \
  "in-cluster updater must redeploy heroku-24 slug apps"
need_in "${ROOT}/controller/types/types_test.go" 'heroku-24 slugrunner-24 must be updated' \
  "unit tests must cover slugrunner-24 update matching"
need_in "${github_updater}" 'EnsureRouterStrategy' \
  "flynn-host update must stop the old host-network router before starting the replacement"
need_in "${incluster}" 'EnsureRouterStrategy' \
  "in-cluster updater must stop the old host-network router before starting the replacement"
need_in "${ROOT}/bootstrap/manifest_template.json" '"strategy": "one-down-one-up"' \
  "bootstrap must create the router with one-down-one-up (host-network :80/:443)"
need_in "${ROOT}/controller/worker/deployment/omni_rolling.go" 'omniRollPlan' \
  "omni one-down-one-up must roll host-network jobs one host at a time"
need_in "${ROOT}/controller/worker/deployment/omni_rolling.go" 'waitOldOmniJobsStopped' \
  "omni rolling must wait for old jobs to release host ports before starting the replacement"
need_in "${ROOT}/controller/scheduler/job.go" 'FormationHostIDsTag' \
  "scheduler must honor flynn-host-ids formation tags for omni rolling"
need_in "${ROOT}/controller/scheduler/formation.go" 'RectifyOmniFunc' \
  "omni counts must multiply by matching hosts when flynn-host-ids tags are set"

echo "ok updater retries HA sirenia new-instance timeout and polls discoverd"

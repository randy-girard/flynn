#!/bin/bash
# Regression: smoke must cover both Dockerfile deploy paths on every topology:
# git-push on the container stack (dockerbuilder-24 + BuildKit + tarreceive)
# and flynn docker push of a pre-built image (local docker save → tarreceive).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
app="${ROOT}/test/apps/upgrade-smoke-docker"

need() {
  local needle=$1 msg=$2
  if ! grep -qE "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_file "${app}/Dockerfile" "smoke Dockerfile app must live in test/apps/upgrade-smoke-docker"
need_file "${app}/start.sh" "Dockerfile app must ship a start script that listens on PORT"
grep -q 'EXPOSE 8080' "${app}/Dockerfile" \
  || { echo "Dockerfile must EXPOSE 8080 (Flynn listen port)" >&2; exit 1; }
grep -q 'alpine' "${app}/Dockerfile" \
  || { echo "Dockerfile must use alpine so apk/httpd are available" >&2; exit 1; }
grep -q 'RUN apk' "${app}/Dockerfile" \
  || { echo "Dockerfile must RUN apk (BuildKit nested runc on dockerbuilder-24)" >&2; exit 1; }
grep -q 'attempt' "${app}/Dockerfile" \
  || { echo "Dockerfile apk must retry so Alpine CDN blips do not fail smoke" >&2; exit 1; }
grep -q 'ARG SMOKE_BODY="docker-smoke ok"' "${app}/Dockerfile" \
  || { echo "Dockerfile ARG SMOKE_BODY default must be quoted (spaces)" >&2; exit 1; }
grep -q 'ENV SMOKE_BODY="${SMOKE_BODY}"' "${app}/Dockerfile" \
  || { echo "Dockerfile ENV SMOKE_BODY must be quoted so the body keeps spaces" >&2; exit 1; }
grep -q 'PORT' "${app}/start.sh" \
  || { echo "start.sh must honor Flynn PORT" >&2; exit 1; }
grep -q 'SMOKE_BODY' "${app}/start.sh" \
  || { echo "start.sh must print SMOKE_BODY so docker-push is distinguishable" >&2; exit 1; }
grep -q 'docker-smoke ok' "${app}/start.sh" \
  || { echo "start.sh must default to a body distinct from the slug app" >&2; exit 1; }

grep -q 'wget' "${app}/Dockerfile" \
  || { echo "Dockerfile must install wget so flynn run can HTTP-get the running app" >&2; exit 1; }

need 'test/apps/upgrade-smoke-docker' \
  "smoke must deploy the Dockerfile app, not only the slug buildpack app"
need 'stack set container' \
  "Dockerfile deploy must switch the app to the container stack"
need 'step_deploy_docker_app' \
  "smoke must have a dedicated Dockerfile git-push step"
need 'step_deploy_docker_push_app' \
  "smoke must flynn docker push a pre-built image, not only git-push a Dockerfile"
need 'ensure_docker_cli_on_node1' \
  "flynn docker push needs a local docker CLI (docker save) on node1"
need '"iptables": false' \
  "docker.io on a Flynn node must not rewrite cluster iptables"
need 'docker push' \
  "smoke must invoke flynn docker push"
need 'docker build --network=host --build-arg SMOKE_BODY' \
  "smoke must docker-build on the host net (daemon.json bridge:none has no container DNS)"
need 'retrying in 10s' \
  "node1 docker build must retry when Alpine apk/CDN flakes"
need 'DOCKER_PUSH_BODY' \
  "docker-push HTTP body must be configurable and distinct from git-push"
need 'wait_and_assert_docker_apps' \
  "every topology verify phase must probe both Dockerfile deploy paths"
need 'docker-http' \
  "smoke must HTTP-probe the Dockerfile git-push app before and after upgrade"
need 'docker-push-http' \
  "smoke must HTTP-probe the flynn docker push app before and after upgrade"
need 'docker-ps' \
  "smoke must show the Dockerfile app process up (not slugrunner /runner/init)"
need 'docker-push-ps' \
  "smoke must show the docker-push app process up"
need 'ps -t app' \
  "docker-ps must list type app (header-only flynn ps after restore is not enough)"
need '\$2=="app"' \
  "docker-ps must match a data row, not CREATED matching -iE up"
need 'scale app=1' \
  "Dockerfile apps use process type app; smoke must scale app=1 after git push"
need 'docker-cli-run' \
  "smoke must flynn run against the Dockerfile app (no /runner/init)"
need 'echo docker-cli' \
  "container flynn run must execute echo in the image"
need 'docker-push-run' \
  "smoke must flynn run against the docker-push app"
need 'echo docker-push-cli' \
  "docker-push flynn run must execute echo in the pre-built image"
need 'cat /start.sh' \
  "container flynn run must read /start.sh from the image"
need 'net-isolate-peer' \
  "smoke must prove Dockerfile jobs cannot reach other apps via discoverd"
need 'slug app git dir missing' \
  "membership must git-push the slug app, not only the Dockerfile app"
need 'smoke_docker_push_image 0' \
  "membership must flynn docker push again after add/remove"

need_file "${ROOT}/dockerbuilder/img/packages.sh" "dockerbuilder packages.sh must exist"
grep -q 'runc' "${ROOT}/dockerbuilder/img/packages.sh" \
  || { echo "dockerbuilder on ubuntu-noble must install runc for BuildKit" >&2; exit 1; }

echo "ok Dockerfile git-push + flynn docker push smoke (dockerbuilder-24 + tarreceive)"

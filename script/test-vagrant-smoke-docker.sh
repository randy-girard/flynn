#!/bin/bash
# Regression: smoke must git-push a Dockerfile app (container stack) so the
# slimmed dockerbuilder-24 image (ubuntu-noble + BuildKit, not heroku-24-build)
# and tarreceive import cannot regress silently.
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
grep -q 'PORT' "${app}/start.sh" \
  || { echo "start.sh must honor Flynn PORT" >&2; exit 1; }
grep -q 'docker-smoke ok' "${app}/start.sh" \
  || { echo "start.sh must print a body distinct from the slug app" >&2; exit 1; }

grep -q 'wget' "${app}/Dockerfile" \
  || { echo "Dockerfile must install wget so flynn run can HTTP-get the running app" >&2; exit 1; }

need 'test/apps/upgrade-smoke-docker' \
  "smoke must deploy the Dockerfile app, not only the slug buildpack app"
need 'stack set container' \
  "Dockerfile deploy must switch the app to the container stack"
need 'step_deploy_docker_app' \
  "smoke must have a dedicated Dockerfile git-push step"
need 'docker-http' \
  "smoke must HTTP-probe the Dockerfile app before and after upgrade"
need 'docker-ps' \
  "smoke must show the Dockerfile app process up (not slugrunner /runner/init)"
need 'scale app=1' \
  "Dockerfile apps use process type app; smoke must scale app=1 after git push"
need 'docker-cli-run' \
  "smoke must flynn run against the Dockerfile app (no /runner/init)"
need 'echo docker-cli' \
  "container flynn run must execute echo in the image"
need 'cat /start.sh' \
  "container flynn run must read /start.sh from the image"
need 'web.discoverd:8080' \
  "container flynn run must hit the running app HTTP via discoverd"

need_file "${ROOT}/dockerbuilder/img/packages.sh" "dockerbuilder packages.sh must exist"
grep -q 'runc' "${ROOT}/dockerbuilder/img/packages.sh" \
  || { echo "dockerbuilder on ubuntu-noble must install runc for BuildKit" >&2; exit 1; }

echo "ok Dockerfile/container-stack smoke deploy (dockerbuilder-24 + tarreceive)"

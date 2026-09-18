#!/bin/bash
# Regression: smoke must git-push an app with a custom .buildpacks file so
# heroku-buildpack-multi + heroku-buildpack-inline still compile after upgrade.
# The Go slug app only exercises the stock Go pack; this proves user-defined
# buildpacks (the documented .buildpacks path) still work.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
app="${ROOT}/test/apps/upgrade-smoke-buildpack"

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

need_file "${app}/.buildpacks" "custom-buildpack smoke app must live in test/apps/upgrade-smoke-buildpack"
need_file "${app}/Procfile" "custom-buildpack app must have a Procfile"
need_file "${app}/web.rb" "custom-buildpack app must ship a ruby HTTP server (slugrunner has ruby)"
need_file "${app}/bin/detect" "inline buildpack requires bin/detect"
need_file "${app}/bin/compile" "inline buildpack requires bin/compile"
need_file "${app}/bin/release" "inline buildpack requires bin/release"

if [[ ! -x "${app}/bin/detect" || ! -x "${app}/bin/compile" || ! -x "${app}/bin/release" ]]; then
  echo "bin/detect, bin/compile, and bin/release must be executable" >&2
  exit 1
fi

grep -q 'heroku-buildpack-inline' "${app}/.buildpacks" \
  || { echo ".buildpacks must name heroku-buildpack-inline (documented custom pack)" >&2; exit 1; }
if grep -q 'BUILDPACK_URL' "${app}/.buildpacks"; then
  echo ".buildpacks must be URL lines, not BUILDPACK_URL env syntax" >&2
  exit 1
fi
grep -q 'custom-buildpack ok' "${app}/bin/compile" \
  || { echo "bin/compile must write custom-buildpack ok so HTTP can prove compile ran" >&2; exit 1; }
grep -q '.buildpack-stamp' "${app}/bin/compile" \
  || { echo "bin/compile must write .buildpack-stamp into the slug" >&2; exit 1; }
grep -q 'PORT' "${app}/web.rb" \
  || { echo "web.rb must honor Flynn PORT" >&2; exit 1; }
grep -q '.buildpack-stamp' "${app}/web.rb" \
  || { echo "web.rb must serve the compile stamp (not a hardcoded body only)" >&2; exit 1; }
grep -q 'ruby web.rb' "${app}/Procfile" \
  || { echo "Procfile must run ruby web.rb (slugrunner already has ruby)" >&2; exit 1; }

need 'test/apps/upgrade-smoke-buildpack' \
  "smoke must git-push the custom .buildpacks app, not only the Go slug app"
need 'BUILDPACK_APP_NAME' \
  "custom-buildpack app name must be configurable"
need 'step_deploy_buildpack_app' \
  "smoke must have a dedicated custom-buildpack git-push step"
need 'wait_and_assert_buildpack_app' \
  "every topology verify phase must probe the custom-buildpack app"
need 'buildpack-http' \
  "smoke must HTTP-probe the custom-buildpack app before and after upgrade"
need 'buildpack-ps' \
  "smoke must show the custom-buildpack web process up"
need 'ps -t web' \
  "buildpack-ps must list type web"
need '\$2=="web"' \
  "buildpack-ps must match a data row, not CREATED matching -iE up"
need 'heroku-buildpack-inline' \
  "deploy must assert .buildpacks names heroku-buildpack-inline"
need 'buildpack-cli-run' \
  "smoke must flynn run against the custom-buildpack slug"
need 'echo buildpack-cli' \
  "custom-buildpack flynn run must execute echo in slugrunner"
need 'buildpack-cli-stamp' \
  "smoke must cat .buildpack-stamp from a one-off to prove compile output is in the slug"
need 'cat .buildpack-stamp' \
  "one-off must read the compile stamp from the slug root"
need 'buildpack app git dir missing' \
  "membership must git-push the custom-buildpack app, not only slug + Dockerfile"
need 'Deploy custom-buildpack app' \
  "SKIP_DEPLOY must also skip the custom-buildpack git-push step"

echo "ok custom .buildpacks git-push smoke (heroku-buildpack-inline + bin/compile)"

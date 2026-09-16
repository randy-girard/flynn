#!/bin/bash
# Regression: GitHub Actions image builds must not treat retried compiler/OOM
# lines as workflow annotations, and must persist/retry without rebuilding
# every image (which looks like a hang on hosted runners).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
wf="${ROOT}/.github/workflows/release.yml"
build="${ROOT}/build.sh"
builder="${ROOT}/builder/build.go"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${wf}" 'remove-matcher owner=go' \
  "release workflow must disable the Go problem matcher during flynn-builder logs"
need "${wf}" 'app image builds \(phase 2; default 2' \
  "release workflow app image concurrency default must be 2 on hosted runners"
need "${wf}" 'FLYNN_GO_BUILD_P' \
  "release workflow must cap go build -p inside overlay jobs"
need "${wf}" 'timeout-minutes: 90' \
  "Build app images must have a step timeout so a stuck overlay job fails instead of hanging"
need "${build}" 'Reducing concurrency' \
  "build.sh must halve flynn-builder concurrency after a failed attempt"
need "${build}" '4GiB' \
  "GitHub Actions must use a lower GOMEMLIMIT so overlay go builds still have RAM"
need "${builder}" 'persisted successful image artifacts for retry' \
  "flynn-builder must write images.json after a partial failure so retries skip cached images"
need "${builder}" 'image builds still running' \
  "flynn-builder must log in-flight images so a stall is visible in CI logs"

echo "ok GitHub Actions image-build hang/annotation regressions"

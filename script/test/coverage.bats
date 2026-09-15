#!/usr/bin/env bats

load "helper"

setup() {
  COVER_TMP="$(mktemp -d "${BATS_TMPDIR}/flynn-cover.XXXXXX")"
}

teardown() {
  rm -rf "${COVER_TMP}"
}

@test "report-unit-coverage merges profiles and writes HTML" {
  cat >"${COVER_TMP}/a.out" <<EOF
mode: atomic
github.com/flynn/flynn/pkg/plugin/manifest.go:96.32,99.2 2 1
EOF
  cat >"${COVER_TMP}/b.out" <<EOF
mode: atomic
github.com/flynn/flynn/pkg/plugin/catalog.go:16.48,23.2 2 0
EOF

  run env COVERAGE_DIR="${COVER_TMP}" "${ROOT}/script/report-unit-coverage" "${COVER_TMP}/a.out" "${COVER_TMP}/b.out"
  assert_success

  [[ -f "${COVER_TMP}/coverage.out" ]]
  [[ -f "${COVER_TMP}/index.html" ]]
  [[ -f "${COVER_TMP}/style.css" ]]
  [[ -f "${COVER_TMP}/files/pkg/plugin/manifest.go.html" ]]
  [[ -f "${COVER_TMP}/files/pkg/plugin/catalog.go.html" ]]
  [[ -f "${COVER_TMP}/func.txt" ]]
  [[ -f "${COVER_TMP}/summary.txt" ]]
  grep -q "pkg/plugin/manifest.go" "${COVER_TMP}/coverage.out"
  grep -q "pkg/plugin/catalog.go" "${COVER_TMP}/coverage.out"
  grep -q "total:" "${COVER_TMP}/summary.txt"
  [[ -f "${COVER_TMP}/badge.svg" ]]
  [[ -f "${COVER_TMP}/shields.json" ]]
  grep -q "coverage" "${COVER_TMP}/badge.svg"
  grep -q "schemaVersion" "${COVER_TMP}/shields.json"
  grep -q "Overall by package area" "${COVER_TMP}/index.html"
  grep -q 'id="pkg"' "${COVER_TMP}/index.html"
  grep -q "files/pkg/plugin/manifest.go.html" "${COVER_TMP}/index.html"
  grep -q "func (m \*Manifest) Validate()" "${COVER_TMP}/files/pkg/plugin/manifest.go.html"
}

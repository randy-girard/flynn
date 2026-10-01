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
github.com/randy-girard/flynn/pkg/plugin/manifest.go:96.32,99.2 2 1
EOF
  cat >"${COVER_TMP}/b.out" <<EOF
mode: atomic
github.com/randy-girard/flynn/pkg/plugin/catalog.go:16.48,23.2 2 0
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

@test "ci-push-coverage-badge retries when origin moved" {
  ORIGIN="$(mktemp -d "${BATS_TMPDIR}/badge-origin.XXXXXX")"
  SEED="$(mktemp -d "${BATS_TMPDIR}/badge-seed.XXXXXX")"
  CI="$(mktemp -d "${BATS_TMPDIR}/badge-ci.XXXXXX")"
  git init --bare -b main "${ORIGIN}"
  git clone "${ORIGIN}" "${SEED}"
  git -C "${SEED}" config user.name test
  git -C "${SEED}" config user.email test@example.com
  mkdir -p "${SEED}/.github/badges"
  echo old >"${SEED}/.github/badges/coverage.svg"
  git -C "${SEED}" add .github/badges/coverage.svg
  git -C "${SEED}" commit -qm init
  git -C "${SEED}" push origin HEAD:main

  git clone "${ORIGIN}" "${CI}"
  git -C "${CI}" config user.name test
  git -C "${CI}" config user.email test@example.com
  mkdir -p "${CI}/coverage"
  printf '<svg>new</svg>\n' >"${CI}/coverage/badge.svg"
  printf '{"schemaVersion":1,"label":"coverage","message":"1%%"}\n' >"${CI}/coverage/shields.json"

  echo raced >"${SEED}/.github/badges/coverage.svg"
  git -C "${SEED}" add .github/badges/coverage.svg
  git -C "${SEED}" commit -qm raced
  git -C "${SEED}" push origin HEAD:main

  run env \
    GITHUB_REF=refs/heads/main \
    GITHUB_TOKEN=x \
    GITHUB_REPOSITORY=randy-girard/flynn \
    COVERAGE_BADGE_ROOT="${CI}" \
    COVERAGE_BADGE_REMOTE="${ORIGIN}" \
    COVERAGE_DIR="${CI}/coverage" \
    "${ROOT}/script/ci-push-coverage-badge"
  assert_success
  git --git-dir="${ORIGIN}" log -1 --format=%s | grep -Fqx "ci: update coverage badge [skip ci]"
  git --git-dir="${ORIGIN}" show HEAD:.github/badges/coverage.svg | grep -q "<svg>new</svg>"
}

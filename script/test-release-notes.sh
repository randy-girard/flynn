#!/bin/bash
# Regression: GitHub Releases (manual script/release and CI) must group
# conventional commits (feat/fix/test/…) instead of a flat git log.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
notes_lib="${ROOT}/script/lib/release-notes.sh"
release="${ROOT}/script/release"
workflow="${ROOT}/.github/workflows/release.yml"

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_file "${notes_lib}" "grouped notes must live in script/lib/release-notes.sh"
need_file "${release}" "script/release must exist"

if ! grep -q 'flynn_categorized_release_notes' "${notes_lib}"; then
  echo "release-notes lib must group commits by conventional type" >&2
  exit 1
fi
if ! grep -q '### ✨ Features' "${notes_lib}"; then
  echo "release-notes lib must keep the Features heading used by manual releases" >&2
  exit 1
fi
if ! grep -q '### 🐛 Bug Fixes' "${notes_lib}"; then
  echo "release-notes lib must keep the Bug Fixes heading used by manual releases" >&2
  exit 1
fi

if ! grep -q -- '--target notes' "${release}"; then
  echo "script/release must support --target notes for CI" >&2
  exit 1
fi
if ! grep -q 'flynn_github_release_notes' "${release}"; then
  echo "script/release github target must use the shared grouped notes body" >&2
  exit 1
fi

if grep -q "git log --pretty=format:'- %s (%h)'" "${workflow}"; then
  echo "GitHub Actions must not write a flat commit list for the release body" >&2
  exit 1
fi
if ! grep -q -- '--target notes' "${workflow}"; then
  echo "release workflow must generate notes via script/release --target notes" >&2
  exit 1
fi

# shellcheck source=/dev/null
source "${notes_lib}"

sample="$(mktemp)"
trap 'rm -f "${sample}"' EXIT
bash "${release}" --target notes --version v20990101.0 --github-repo randy-girard/flynn --output "${sample}" >/dev/null
# Any conventional-commit group counts: a docs-only or chore-only range emits
# just its own heading, which used to fail this gate on the docs commit.
if ! grep -qE '^### ' "${sample}"; then
  echo "generated notes must include at least one conventional-commit group" >&2
  head -n 40 "${sample}" >&2
  exit 1
fi
if ! grep -q '## Install Flynn CLI' "${sample}"; then
  echo "generated notes must keep the install sections from the manual release" >&2
  exit 1
fi
if grep -q 'Built from' "${sample}"; then
  echo "CI notes must not replace grouped changes with a Built from SHA blurb" >&2
  exit 1
fi
if ! grep -q 'flynn_omit_coverage_badge_notes' "${notes_lib}"; then
  echo "release-notes lib must filter coverage-badge commits" >&2
  exit 1
fi
filtered="$(printf '%s\n' '- ci: add dispatch_plugins (abc123)' '- ci: update coverage badge [skip ci] (def456)' | flynn_omit_coverage_badge_notes)"
if ! printf '%s\n' "${filtered}" | grep -q 'dispatch_plugins'; then
  echo "coverage-badge filter must keep other ci commits" >&2
  exit 1
fi
if printf '%s\n' "${filtered}" | grep -qi 'ci: update coverage badge'; then
  echo "coverage-badge filter must drop update coverage badge commits" >&2
  exit 1
fi
if grep -qi 'ci: update coverage badge' "${sample}"; then
  echo "generated notes must omit coverage badge commits" >&2
  grep -i 'ci: update coverage badge' "${sample}" >&2
  exit 1
fi
if ! grep -q 'sort -V' "${notes_lib}"; then
  echo "release-notes lib must pick the previous tag by version, not git describe HEAD^" >&2
  exit 1
fi
if grep -q 'describe --tags --abbrev=0 --match' "${notes_lib}"; then
  echo "release-notes lib must not use git describe for the previous tag (wrong tag on topic branches)" >&2
  exit 1
fi

scratch="$(mktemp -d)"
trap 'rm -f "${sample}"; rm -rf "${scratch}"' EXIT
git -C "${scratch}" init -q
git -C "${scratch}" config user.email test@example.com
git -C "${scratch}" config user.name test
git -C "${scratch}" config commit.gpgsign false
git -C "${scratch}" commit -q --allow-empty -m "feat: one"
git -C "${scratch}" tag v20260101.0
git -C "${scratch}" commit -q --allow-empty -m "feat: two"
git -C "${scratch}" tag v20260102.0
git -C "${scratch}" checkout -q -b topic
git -C "${scratch}" commit -q --allow-empty -m "feat: topic-only"
# shellcheck source=/dev/null
source "${notes_lib}"
(
  cd "${scratch}"
  prev="$(flynn_previous_release_tag v20260102.0)"
  range="$(flynn_commit_range_since_previous v20260102.0)"
  notes="$(flynn_categorized_release_notes "${range}")"
  if [[ "${prev}" != "v20260101.0" ]]; then
    echo "previous tag for v20260102.0 should be v20260101.0, got ${prev}" >&2
    exit 1
  fi
  if [[ "${range}" != "v20260101.0..v20260102.0" ]]; then
    echo "range should be previous..this tag, got ${range}" >&2
    exit 1
  fi
  if printf '%s' "${notes}" | grep -q topic-only; then
    echo "notes for v20260102.0 must not include topic-branch commits" >&2
    printf '%s\n' "${notes}" >&2
    exit 1
  fi
  if ! printf '%s' "${notes}" | grep -q 'feat: two'; then
    echo "notes for v20260102.0 must include commits since the previous tag" >&2
    printf '%s\n' "${notes}" >&2
    exit 1
  fi
)

echo "ok GitHub release notes are grouped like script/release"

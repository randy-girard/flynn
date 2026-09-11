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
if ! grep -q '### ✨ Features\|### 🐛 Bug Fixes\|### 🧪 Tests\|### 👷 CI\|### 📦 Other Changes' "${sample}"; then
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

echo "ok GitHub release notes are grouped like script/release"

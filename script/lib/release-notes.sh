#!/bin/bash
# Shared GitHub release-note body used by script/release and CI.
# Groups conventional commits (feat/fix/docs/…) the same way a manual
# ./script/release --target github run does.
#
# shellcheck shell=bash

# Previous published v* tag, independent of the current branch. git describe
# from the parent of HEAD follows ancestry and can pick the wrong tag on a
# topic or release branch. We take the newest CalVer tag strictly older than
# VERSION (or the newest tag when VERSION is empty / not yet tagged).
flynn_previous_release_tag() {
  local version=${1:-}
  local tag
  while IFS= read -r tag; do
    [[ -z "${tag}" ]] && continue
    if [[ -n "${version}" && "${tag}" == "${version}" ]]; then
      continue
    fi
    if [[ -n "${version}" ]]; then
      if [[ "$(printf '%s\n%s\n' "${tag}" "${version}" | sort -V | tail -n1)" != "${version}" ]]; then
        continue
      fi
    fi
    printf '%s' "${tag}"
    return 0
  done < <(git tag -l 'v*' 2>/dev/null | sort -V -r)
}

# Commits that landed in this release: previous tag .. this tag (or HEAD
# when VERSION is not a git revision yet). Never walks the current branch
# against an unrelated ancestor.
flynn_commit_range_since_previous() {
  local version=${1:-}
  local prev head
  prev="$(flynn_previous_release_tag "${version}")"
  head="${version}"
  if [[ -z "${head}" ]] || ! git rev-parse --verify --quiet "${head}^{commit}" >/dev/null 2>&1; then
    head="HEAD"
  fi
  if [[ -n "${prev}" ]]; then
    printf '%s' "${prev}..${head}"
  else
    printf '%s' "${head}"
  fi
}

# Drop automated coverage-badge commits from GitHub release bodies.
flynn_omit_coverage_badge_notes() {
  grep -viF -- 'ci: update coverage badge' || true
}

# Print categorized markdown for COMMIT_RANGE (e.g. v20260714.0..HEAD).
flynn_categorized_release_notes() {
  local range=$1
  local feat fix chore docs refactor perf testc build ci other notes=""

  feat="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^feat" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  fix="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^fix" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  chore="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^chore" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  docs="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^docs" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  refactor="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^refactor" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  perf="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^perf" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  testc="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^test" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  build="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^build" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  ci="$(git log --no-merges --pretty=format:"- %s (%h)" --grep="^ci" "${range}" 2>/dev/null | flynn_omit_coverage_badge_notes || true)"
  other="$(git log --no-merges --pretty=format:"- %s (%h)" "${range}" 2>/dev/null | grep -v -E "^- (feat|fix|chore|docs|refactor|perf|test|build|ci)" | flynn_omit_coverage_badge_notes || true)"

  if [[ -n "${feat}" ]]; then
    notes+="### ✨ Features

${feat}

"
  fi
  if [[ -n "${fix}" ]]; then
    notes+="### 🐛 Bug Fixes

${fix}

"
  fi
  if [[ -n "${perf}" ]]; then
    notes+="### ⚡ Performance

${perf}

"
  fi
  if [[ -n "${refactor}" ]]; then
    notes+="### ♻️ Refactoring

${refactor}

"
  fi
  if [[ -n "${docs}" ]]; then
    notes+="### 📚 Documentation

${docs}

"
  fi
  if [[ -n "${testc}" ]]; then
    notes+="### 🧪 Tests

${testc}

"
  fi
  if [[ -n "${build}" ]]; then
    notes+="### 🏗️ Build

${build}

"
  fi
  if [[ -n "${ci}" ]]; then
    notes+="### 👷 CI

${ci}

"
  fi
  if [[ -n "${chore}" ]]; then
    notes+="### 🔧 Chores

${chore}

"
  fi
  if [[ -n "${other}" ]]; then
    notes+="### 📦 Other Changes

${other}

"
  fi

  if [[ -z "${notes}" ]]; then
    notes="No changes recorded."
  fi
  printf '%s' "${notes}"
}

# Full GitHub release body (changelog groups + install/artifact sections).
flynn_github_release_notes() {
  local version=$1
  local repo=$2
  local prev range compare changelog categorized
  prev="$(flynn_previous_release_tag "${version}")"
  range="$(flynn_commit_range_since_previous "${version}")"
  compare="${prev:-initial}...${version}"
  changelog="https://github.com/${repo}/compare/${compare}"
  categorized="$(flynn_categorized_release_notes "${range}")"

  cat <<EOF
## Flynn ${version}

**Full Changelog**: [${compare}](${changelog})

${categorized}
---

## Install Flynn CLI

Install the Flynn command-line interface on your local machine (Linux or macOS):

\`\`\`bash
curl -fsSL https://github.com/${repo}/releases/download/${version}/install-flynn-cli | sudo bash
\`\`\`

## Install Flynn Host (Server)

Install Flynn on an Ubuntu server (24.04):

\`\`\`bash
curl -fsSL https://github.com/${repo}/releases/download/${version}/install-flynn | sudo bash
\`\`\`

## Artifacts

### CLI Binaries
| Binary | Platform |
|--------|----------|
| flynn-linux-amd64.gz | Linux (x86_64) |
| flynn-linux-arm64.gz | Linux (ARM64) |
| flynn-darwin-amd64.gz | macOS (Intel) |
| flynn-darwin-arm64.gz | macOS (Apple Silicon) |
| flynn-windows-amd64.exe.gz | Windows (x86_64) |

### Server Binaries
| Binary | Description |
|--------|-------------|
| flynn-host-linux-amd64.gz | Flynn host daemon (Linux x86_64) |
| flynn-init-linux-amd64.gz | Flynn init binary (Linux x86_64) |

### Other
| File | Description |
|------|-------------|
| install-flynn-cli | CLI installer script |
| install-flynn | Server installer script |
| checksums.sha512 | SHA512 checksums for all artifacts |
EOF
}

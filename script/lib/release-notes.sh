#!/bin/bash
# Shared GitHub release-note body used by script/release and CI.
# Groups conventional commits (feat/fix/docs/…) the same way a manual
# ./script/release --target github run does.
#
# shellcheck shell=bash

flynn_previous_release_tag() {
  local version=${1:-}
  local prev=""
  prev="$(git describe --tags --abbrev=0 --match 'v*' HEAD^ 2>/dev/null || true)"
  if [[ -n "${version}" && "${prev}" == "${version}" ]]; then
    prev="$(git describe --tags --abbrev=0 --match 'v*' "${version}^" 2>/dev/null || true)"
  fi
  printf '%s' "${prev}"
}

flynn_commit_range_since_previous() {
  local version=${1:-}
  local prev
  prev="$(flynn_previous_release_tag "${version}")"
  if [[ -n "${prev}" ]]; then
    printf '%s' "${prev}..HEAD"
  else
    printf '%s' "HEAD~20..HEAD"
  fi
}

# Print categorized markdown for COMMIT_RANGE (e.g. v20260714.0..HEAD).
flynn_categorized_release_notes() {
  local range=$1
  local feat fix chore docs refactor perf testc build ci other notes=""

  feat="$(git log --pretty=format:"- %s (%h)" --grep="^feat" "${range}" 2>/dev/null || true)"
  fix="$(git log --pretty=format:"- %s (%h)" --grep="^fix" "${range}" 2>/dev/null || true)"
  chore="$(git log --pretty=format:"- %s (%h)" --grep="^chore" "${range}" 2>/dev/null || true)"
  docs="$(git log --pretty=format:"- %s (%h)" --grep="^docs" "${range}" 2>/dev/null || true)"
  refactor="$(git log --pretty=format:"- %s (%h)" --grep="^refactor" "${range}" 2>/dev/null || true)"
  perf="$(git log --pretty=format:"- %s (%h)" --grep="^perf" "${range}" 2>/dev/null || true)"
  testc="$(git log --pretty=format:"- %s (%h)" --grep="^test" "${range}" 2>/dev/null || true)"
  build="$(git log --pretty=format:"- %s (%h)" --grep="^build" "${range}" 2>/dev/null || true)"
  ci="$(git log --pretty=format:"- %s (%h)" --grep="^ci" "${range}" 2>/dev/null || true)"
  other="$(git log --pretty=format:"- %s (%h)" "${range}" 2>/dev/null | grep -v -E "^- (feat|fix|chore|docs|refactor|perf|test|build|ci)" || true)"

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
| flynn-linux-386.gz | Linux (x86) |
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

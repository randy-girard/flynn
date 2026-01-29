#!/bin/bash
#
# Flynn Build Script
# Builds Flynn components and optionally pushes to GitHub Releases and/or TUF server
#
# Usage:
#   ./build.sh                              # Build only (no upload)
#   ./build.sh --tuf-release                # Build and push to TUF server
#   ./build.sh --github-release             # Build and push to GitHub Releases
#   ./build.sh --tuf-release --github-release  # Push to both
#   ./build.sh --github-release --version v20240127.0  # Specific version
#

set -eo pipefail

# Parse command line arguments
PUSH_TO_GITHUB=false
PUSH_TO_TUF=false
VERSION=""
GITHUB_REPO="${FLYNN_GITHUB_REPO:-randy-girard/flynn}"
TUF_REMOTE_HOST="${TUF_REMOTE_HOST:-root@10.0.0.211}"
TUF_REMOTE_PATH="${TUF_REMOTE_PATH:-/root/go-tuf/repo}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --github-release)
      PUSH_TO_GITHUB=true
      shift
      ;;
    --tuf-release)
      PUSH_TO_TUF=true
      shift
      ;;
    --version)
      VERSION="$2"
      shift 2
      ;;
    --github-repo)
      GITHUB_REPO="$2"
      shift 2
      ;;
    --tuf-host)
      TUF_REMOTE_HOST="$2"
      shift 2
      ;;
    --tuf-path)
      TUF_REMOTE_PATH="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [OPTIONS]"
      echo ""
      echo "OPTIONS:"
      echo "  --tuf-release              Push to TUF remote server"
      echo "  --github-release           Push to GitHub Releases"
      echo "  --version VERSION          Version for GitHub release (e.g., v20240127)"
      echo "  --github-repo OWNER/REPO   GitHub repository [default: randy-girard/flynn]"
      echo "  --tuf-host USER@HOST       TUF remote host [default: root@10.0.0.211]"
      echo "  --tuf-path PATH            TUF remote path [default: /root/go-tuf/repo]"
      exit 1
      ;;
  esac
done

# Generate version if not provided
if [[ -z "${VERSION}" ]]; then
  # Format: vYYYYMMDD.N where N is incremented if multiple releases on same day
  DATE_PREFIX="v$(date +%Y%m%d)"
  # Check for existing tags with today's date
  LATEST_TODAY=$(git tag -l "${DATE_PREFIX}.*" 2>/dev/null | sort -V | tail -n1)
  if [[ -n "${LATEST_TODAY}" ]]; then
    # Extract the iteration number and increment
    ITERATION="${LATEST_TODAY##*.}"
    VERSION="${DATE_PREFIX}.$((ITERATION + 1))"
  else
    VERSION="${DATE_PREFIX}.0"
  fi
  echo "===> Auto-generated version: ${VERSION}"
fi

export PATH=/usr/local/go/bin:$PATH
export HOST_UBUNTU=$(lsb_release -cs)
export TUF_ROOT_PASSPHRASE="password"
export TUF_TARGETS_PASSPHRASE="password"
export TUF_SNAPSHOT_PASSPHRASE="password"
export TUF_TIMESTAMP_PASSPHRASE="password"
export PATH="/root/go/src/github.com/flynn/flynn/build/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=1
export CLUSTER_DOMAIN=flynn.local
export DISCOVERD=192.0.2.200:1111
export DISCOVERY_SERVER=http://localhost:8180
export EXTERNAL_IP=192.0.2.200
export LISTEN_IP=192.0.2.200
export PORT_0=1111
export DISCOVERD_PEERS=192.0.2.200:1111
export TELEMETRY_URL=http://localhost:8080/measure/scheduler
export FLYNN_REPOSITORY=http://localhost:8080
export SQUASHFS="/var/lib/flynn/base-layer.squashfs"
export JSON_FILE="/root/go/src/github.com/flynn/flynn/builder/manifest.json"
export UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

echo "GO VERSION"
echo "$(go version)"

./script/stop-all
./script/install-flynn --remove --clean --yes

echo 'Acquire::ForceIPv4 "true";' | sudo tee /etc/apt/apt.conf.d/99force-ipv4

ssh -o StrictHostKeyChecking=no "${TUF_REMOTE_HOST}" "rm -rf ${TUF_REMOTE_PATH}/*"

mkdir -p /var/lib/flynn/base-root
debootstrap \
  --variant=minbase \
  --include=squashfs-tools,curl,gnupg,ca-certificates,bash \
  $UBUNTU_CODENAME \
  /var/lib/flynn/base-root \
  http://mirror.math.princeton.edu/pub/ubuntu
mksquashfs /var/lib/flynn/base-root "$SQUASHFS" -noappend

export SIZE=$(stat -c%s "$SQUASHFS")
export HASH=$(./sha512_256_binary "$SQUASHFS")

echo "SIZE=$SIZE"
echo "HASH=$HASH"

# Update JSON file using jq
jq --arg url "file://$SQUASHFS" \
  --arg size "$SIZE" \
  --arg hash "$HASH" \
  '.base_layer.url = $url |
    .base_layer.size = ($size | tonumber) |
    .base_layer.hashes.sha512_256 = $hash' \
  "$JSON_FILE" > "${JSON_FILE}.tmp" && mv "${JSON_FILE}.tmp" "$JSON_FILE"

cd /root/go/src/github.com/flynn/go-tuf/ && \
docker compose down && \
rm -rf repo && \
docker compose up -d --build

# Whenever the keys expire, you have to run this
# script again, and then clean and build flynn
./update_keys_in_flynn.sh

scp -o StrictHostKeyChecking=no -r ./repo/* root@10.0.0.211:/root/go-tuf/repo/

cd /root/go/src/github.com/flynn/flynn-discovery && \
docker compose down && \
docker compose up -d --build

cd /root/go/src/github.com/flynn/flynn && \
mkdir -p /etc/flynn && \
mkdir -p /tmp/discoverd-data


rm -rf /tmp/flynn-* && \
rm -rf /var/log/flynn/* && \
make clean && \
./script/build-flynn --version "${VERSION}" && \
rm -f build/bin/flynn-builder && \
rm -f build/bin/flannel-wrapper && \
go build -o build/bin/flannel-wrapper ./flannel/wrapper && \
export DISCOVERY_URL=`./build/bin/flynn-host init --init-discovery` && \
./script/start-all && \
zfs set sync=disabled flynn-default && \
zfs set reservation=512M flynn-default && \
zfs set refreservation=512M flynn-default && \
rm -rf /etc/flynn/tuf.db

# Flynn builder step with retry loop
while true; do
  echo "===> Running flynn-builder build with version: ${VERSION}"
  if ./script/flynn-builder build --version="${VERSION}" --tuf-db=/etc/flynn/tuf.db --verbose; then
    echo "===> flynn-builder build succeeded!"
    break
  else
    echo ""
    echo "===> flynn-builder build FAILED!"
    echo ""
    echo "Press 'r' to retry, or 'q' to quit:"
    read -n 1 -r choice
    echo ""
    case "$choice" in
      r|R)
        echo "===> Retrying flynn-builder build..."
        ;;
      q|Q|*)
        echo "===> Exiting."
        exit 1
        ;;
    esac
  fi
done

./script/export-components --host host0 /root/go/src/github.com/flynn/flynn/go-tuf/repo && \
flynn-host ps -a

cd /root/go/src/github.com/flynn/flynn
cp ./script/install-flynn /usr/bin/install-flynn

echo "===> Build and TUF export complete!"

# ============================================================================
# TUF Remote Server Push (optional)
# ============================================================================
if $PUSH_TO_TUF; then
  echo "===> Pushing to TUF remote server ${TUF_REMOTE_HOST}:${TUF_REMOTE_PATH}..."

  scp -o StrictHostKeyChecking=no -r /root/go/src/github.com/flynn/flynn/go-tuf/repo/repository/ "${TUF_REMOTE_HOST}:${TUF_REMOTE_PATH}/"
  scp -o StrictHostKeyChecking=no /usr/bin/install-flynn "${TUF_REMOTE_HOST}:${TUF_REMOTE_PATH}/install-flynn"

  echo "===> TUF remote push complete!"
fi

# ============================================================================
# GitHub Release Push (optional)
# ============================================================================
if $PUSH_TO_GITHUB; then
  echo "===> Preparing GitHub Release..."

  # Check for GitHub CLI
  if ! command -v gh &> /dev/null; then
    echo "ERROR: GitHub CLI (gh) is not installed. Install it with: apt install gh"
    echo "Then authenticate with: gh auth login"
    exit 1
  fi

  # Check if authenticated
  if ! gh auth status &> /dev/null; then
    echo "ERROR: Not authenticated with GitHub. Run: gh auth login"
    exit 1
  fi

  # Determine version
  if [[ -z "${VERSION}" ]]; then
    # Get version from flynn-host
    VERSION=$(./build/bin/flynn-host version | cut -d'-' -f1)
    if [[ -z "${VERSION}" ]]; then
      VERSION="v$(date +%Y.%m.%d.0)"
    fi
  fi

  echo "===> Creating GitHub Release ${VERSION} for ${GITHUB_REPO}"

  # Create release artifacts directory
  RELEASE_DIR="/tmp/flynn-github-release-${VERSION}"
  rm -rf "${RELEASE_DIR}"
  mkdir -p "${RELEASE_DIR}"

  TUF_STAGED="/root/go/src/github.com/flynn/flynn/go-tuf/repo/staged/targets"

  # Package binaries (gzipped)
  echo "===> Packaging binaries..."
  for bin in flynn-host flynn-init flynn-linux-amd64 flynn-linux-386 flynn-darwin-amd64; do
    if [[ -f "${TUF_STAGED}/${bin}.gz" ]]; then
      cp "${TUF_STAGED}/${bin}.gz" "${RELEASE_DIR}/${bin}.gz"
      echo "  - ${bin}.gz"
    elif [[ -f "./build/bin/${bin}" ]]; then
      gzip -c "./build/bin/${bin}" > "${RELEASE_DIR}/${bin}.gz"
      echo "  - ${bin}.gz (from build/bin)"
    fi
  done

  # Package manifests (gzipped)
  echo "===> Packaging manifests..."
  for manifest in bootstrap-manifest.json images.json; do
    # Look in versioned directory first
    if [[ -f "${TUF_STAGED}/${VERSION}/${manifest}.gz" ]]; then
      cp "${TUF_STAGED}/${VERSION}/${manifest}.gz" "${RELEASE_DIR}/${manifest}.gz"
      echo "  - ${manifest}.gz"
    elif [[ -f "./build/manifests/${manifest}" ]]; then
      gzip -c "./build/manifests/${manifest}" > "${RELEASE_DIR}/${manifest}.gz"
      echo "  - ${manifest}.gz (from build/manifests)"
    fi
  done

  # Package container image manifests
  echo "===> Packaging image manifests..."
  if [[ -d "${TUF_STAGED}/images" ]] && [[ -n "$(ls -A ${TUF_STAGED}/images/*.json 2>/dev/null)" ]]; then
    mkdir -p "${RELEASE_DIR}/images"
    cp -r "${TUF_STAGED}/images/"*.json "${RELEASE_DIR}/images/" 2>/dev/null || true
    # Create a tarball of all image manifests
    (cd "${TUF_STAGED}" && tar czf "${RELEASE_DIR}/images.tar.gz" images/)
    echo "  - images.tar.gz [from TUF staged]"
  elif [[ -d "build/image" ]] && [[ -n "$(ls -A build/image/*.json 2>/dev/null)" ]]; then
    # Fallback: use image manifests from build/image directory
    echo "  (TUF staged images not found, using build/image)"
    mkdir -p "${RELEASE_DIR}/images"
    cp build/image/*.json "${RELEASE_DIR}/images/" 2>/dev/null || true
    # Create a tarball of all image manifests
    (cd "${RELEASE_DIR}" && tar czf "images.tar.gz" images/)
    echo "  - images.tar.gz [from build/image]"
  else
    echo "  WARNING: No image manifests found in ${TUF_STAGED}/images or build/image"
  fi

  # Package squashfs layers (as individual files, not tar - GitHub has 2GB limit)
  echo "===> Packaging layers..."
  LAYERS_DIR="${RELEASE_DIR}/layers"
  mkdir -p "${LAYERS_DIR}"
  LAYER_COUNT=0

  if [[ -d "${TUF_STAGED}/layers" ]] && [[ -n "$(ls -A ${TUF_STAGED}/layers 2>/dev/null)" ]]; then
    # Copy layers from TUF staged directory
    for layer_file in "${TUF_STAGED}/layers"/*; do
      if [[ -f "$layer_file" ]]; then
        cp "$layer_file" "${LAYERS_DIR}/"
        LAYER_COUNT=$((LAYER_COUNT + 1))
      fi
    done
    echo "  - ${LAYER_COUNT} layer files [from TUF staged]"
  elif [[ -d "/var/lib/flynn/layer-cache" ]] && [[ -n "$(ls -A /var/lib/flynn/layer-cache/*.squashfs 2>/dev/null)" ]]; then
    # Fallback: copy layers from the layer-cache directory
    echo "  (TUF staged layers not found, using layer-cache)"
    for layer_file in /var/lib/flynn/layer-cache/*.squashfs /var/lib/flynn/layer-cache/*.json; do
      if [[ -f "$layer_file" ]]; then
        cp "$layer_file" "${LAYERS_DIR}/"
        LAYER_COUNT=$((LAYER_COUNT + 1))
      fi
    done
    echo "  - ${LAYER_COUNT} layer files [from layer-cache]"
  else
    echo "  WARNING: No layers found in ${TUF_STAGED}/layers or /var/lib/flynn/layer-cache"
  fi

  # Show layer sizes
  if [[ ${LAYER_COUNT} -gt 0 ]]; then
    echo "  Layer files:"
    ls -lh "${LAYERS_DIR}" | tail -n +2 | while read line; do
      echo "    $line"
    done
    echo "  Total: $(du -sh ${LAYERS_DIR} | cut -f1)"
  fi

  # Copy install scripts
  cp ./script/install-flynn-github "${RELEASE_DIR}/install-flynn-github"
  cp ./script/install-flynn-cli "${RELEASE_DIR}/install-flynn-cli"

  # Generate checksums (including layer files)
  echo "===> Generating checksums..."
  (cd "${RELEASE_DIR}" && find . -type f -name "*.gz" -o -name "*.squashfs" -o -name "*.json" -o -name "install-flynn-*" | xargs sha512sum > checksums.sha512 2>/dev/null || true)

  echo "===> Release artifacts:"
  ls -lah "${RELEASE_DIR}"
  if [[ -d "${LAYERS_DIR}" ]]; then
    echo "===> Layer files (${LAYER_COUNT} files):"
    du -sh "${LAYERS_DIR}"
  fi

  # Create or update GitHub release
  echo "===> Pushing to GitHub Release ${VERSION}..."

  # Generate release notes from commits since last release
  echo "===> Generating release notes from commits..."
  PREVIOUS_TAG=$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || echo "")
  if [[ -n "${PREVIOUS_TAG}" ]]; then
    echo "  Previous release: ${PREVIOUS_TAG}"
    COMMIT_RANGE="${PREVIOUS_TAG}..HEAD"
    COMMIT_COUNT=$(git rev-list --count "${COMMIT_RANGE}" 2>/dev/null || echo "0")
    echo "  Found ${COMMIT_COUNT} commits since ${PREVIOUS_TAG}"
  else
    echo "  No previous tag found, using recent commits"
    COMMIT_RANGE="HEAD~20..HEAD"
    COMMIT_COUNT="recent"
  fi

  # Categorize commits by conventional commit type
  FEAT_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^feat" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  FIX_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^fix" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  CHORE_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^chore" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  DOCS_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^docs" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  REFACTOR_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^refactor" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  PERF_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^perf" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  TEST_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^test" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  BUILD_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^build" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  CI_COMMITS=$(git log --pretty=format:"- %s (%h)" --grep="^ci" "${COMMIT_RANGE}" 2>/dev/null || echo "")
  # Other commits (don't match any conventional prefix)
  OTHER_COMMITS=$(git log --pretty=format:"- %s (%h)" "${COMMIT_RANGE}" 2>/dev/null | grep -v -E "^- (feat|fix|chore|docs|refactor|perf|test|build|ci)" || echo "")

  # Build categorized release notes
  RELEASE_NOTES=""
  if [[ -n "${FEAT_COMMITS}" ]]; then
    RELEASE_NOTES+="### ✨ Features

${FEAT_COMMITS}

"
  fi
  if [[ -n "${FIX_COMMITS}" ]]; then
    RELEASE_NOTES+="### 🐛 Bug Fixes

${FIX_COMMITS}

"
  fi
  if [[ -n "${PERF_COMMITS}" ]]; then
    RELEASE_NOTES+="### ⚡ Performance

${PERF_COMMITS}

"
  fi
  if [[ -n "${REFACTOR_COMMITS}" ]]; then
    RELEASE_NOTES+="### ♻️ Refactoring

${REFACTOR_COMMITS}

"
  fi
  if [[ -n "${DOCS_COMMITS}" ]]; then
    RELEASE_NOTES+="### 📚 Documentation

${DOCS_COMMITS}

"
  fi
  if [[ -n "${TEST_COMMITS}" ]]; then
    RELEASE_NOTES+="### 🧪 Tests

${TEST_COMMITS}

"
  fi
  if [[ -n "${BUILD_COMMITS}" ]]; then
    RELEASE_NOTES+="### 🏗️ Build

${BUILD_COMMITS}

"
  fi
  if [[ -n "${CI_COMMITS}" ]]; then
    RELEASE_NOTES+="### 👷 CI

${CI_COMMITS}

"
  fi
  if [[ -n "${CHORE_COMMITS}" ]]; then
    RELEASE_NOTES+="### 🔧 Chores

${CHORE_COMMITS}

"
  fi
  if [[ -n "${OTHER_COMMITS}" ]]; then
    RELEASE_NOTES+="### 📦 Other Changes

${OTHER_COMMITS}

"
  fi

  # Fallback if no commits found
  if [[ -z "${RELEASE_NOTES}" ]]; then
    RELEASE_NOTES="No changes recorded."
  fi

  # Check if release already exists
  if gh release view "${VERSION}" --repo "${GITHUB_REPO}" &> /dev/null; then
    echo "  Release ${VERSION} exists, updating..."
    gh release delete "${VERSION}" --repo "${GITHUB_REPO}" --yes || true
  fi

  # Create the release with main artifacts first
  gh release create "${VERSION}" \
    --repo "${GITHUB_REPO}" \
    --title "Flynn ${VERSION}" \
    --notes "## Flynn ${VERSION}

**Full Changelog**: ${PREVIOUS_TAG:-initial}...${VERSION}

${RELEASE_NOTES}
---

## Install Flynn CLI

Install the Flynn command-line interface on your local machine (Linux or macOS):

\`\`\`bash
curl -fsSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/script/install-flynn-cli | sudo bash
\`\`\`

Or install a specific version:

\`\`\`bash
curl -fsSL https://raw.githubusercontent.com/${GITHUB_REPO}/main/script/install-flynn-cli | sudo bash -s -- --version ${VERSION}
\`\`\`

## Install Flynn Host (Server)

Install Flynn on an Ubuntu server (24.04):

\`\`\`bash
curl -fsSL https://github.com/${GITHUB_REPO}/releases/download/${VERSION}/install-flynn-github | sudo bash
\`\`\`

## Artifacts

### CLI Binaries
- \`flynn-linux-amd64.gz\` - Flynn CLI (Linux x86_64)
- \`flynn-linux-386.gz\` - Flynn CLI (Linux x86)
- \`flynn-darwin-amd64.gz\` - Flynn CLI (macOS x86_64)

### Server Binaries
- \`flynn-host.gz\` - Flynn host daemon
- \`flynn-init.gz\` - Flynn init binary

### Manifests & Images
- \`bootstrap-manifest.json.gz\` - Bootstrap manifest
- \`images.json.gz\` - Images manifest
- \`images.tar.gz\` - Container image manifests
- \`layers/*.squashfs\` - Container image layers (individual files)

### Other
- \`install-flynn-cli\` - CLI installer script
- \`install-flynn-github\` - Server installer script
- \`checksums.sha512\` - SHA512 checksums for all artifacts
" \
    "${RELEASE_DIR}"/*.gz "${RELEASE_DIR}"/*.sha512 "${RELEASE_DIR}"/install-flynn-* 2>/dev/null || true

  # Upload layer files separately (they can be large)
  if [[ -d "${LAYERS_DIR}" ]] && [[ -n "$(ls -A ${LAYERS_DIR} 2>/dev/null)" ]]; then
    echo "===> Uploading layer files..."
    for layer_file in "${LAYERS_DIR}"/*; do
      if [[ -f "$layer_file" ]]; then
        layer_name=$(basename "$layer_file")
        echo "  Uploading ${layer_name}..."
        gh release upload "${VERSION}" "$layer_file" --repo "${GITHUB_REPO}" --clobber || {
          echo "  WARNING: Failed to upload ${layer_name}"
        }
      fi
    done
  fi

  echo "===> GitHub Release ${VERSION} created successfully!"
  echo "     https://github.com/${GITHUB_REPO}/releases/tag/${VERSION}"

  # Cleanup
  rm -rf "${RELEASE_DIR}"
fi

echo "===> Build complete!"
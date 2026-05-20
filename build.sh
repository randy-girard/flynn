#!/bin/bash
#
# Flynn Build Script
# Builds Flynn components
#
# Usage:
#   ./build.sh [OPTIONS] [PHASE]
#
# PHASE (default: all):
#   base     Build the debootstrap base root, squashfs, and update builder/manifest.json.
#            Slow; only re-run when changing Ubuntu series or base packages.
#   cluster  Stop Flynn, clean install, compile, start services, run flynn-builder,
#            then stop all local Flynn services again.
#   all      Run base then cluster (same as the historical single-shot build).
#
# Examples:
#   ./build.sh --version v20240127.0 base
#   ./build.sh cluster
#   ./build.sh                              # all phases, auto version
#
# For GitHub Releases, run ./script/github-release after committing your changes.
#

set -eo pipefail

usage() {
  cat <<USAGE >&2
Usage: $0 [OPTIONS] [PHASE]

OPTIONS:
  --version VERSION   Version for build (e.g., v20240127.0)
  -h, --help          Show this message

PHASE (default: all):
  base      Debootstrap + base squashfs + manifest (run rarely)
  cluster   Teardown, build, flynn-builder, then stop all local Flynn services
  all       base then cluster

Examples:
  $0 base
  $0 --version v20240127.0 cluster
USAGE
}

# Get the root directory of the Flynn project
FLYNN_ROOT="$(cd "$(dirname "$0")" && pwd)"
export FLYNN_ROOT

# Parse command line arguments
VERSION=""
PHASE="all"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      if [[ -z "${2:-}" ]]; then
        echo "ERROR: --version requires an argument" >&2
        usage
        exit 1
      fi
      VERSION="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    base|cluster|all)
      PHASE="$1"
      shift
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

# Generate version if not provided
if [[ -z "${VERSION}" ]]; then
  # Format: vYYYYMMDD.N where N is incremented if multiple releases on same day
  DATE_PREFIX="v$(date +%Y%m%d)"
  # Fetch latest tags from remote to ensure we have the most up-to-date tag list
  echo "===> Fetching latest tags from remote..."
  git fetch --tags --force 2>/dev/null || echo "Warning: Could not fetch tags from remote"
  # Check for existing tags with today's date
  LATEST_TODAY=$(git tag -l "${DATE_PREFIX}.*" 2>/dev/null | sort -V | tail -n1)
  if [[ -n "${LATEST_TODAY}" ]]; then
    # Extract the iteration number and increment
    ITERATION="${LATEST_TODAY##*.}"
    VERSION="${DATE_PREFIX}.$((ITERATION + 1))"
  else
    VERSION="${DATE_PREFIX}.0"
  fi
fi

echo "===> Building version: ${VERSION} (phase: ${PHASE})"

# Export FLYNN_VERSION so it's available to all subprocesses
export FLYNN_VERSION="${VERSION}"

export PATH=/usr/local/go/bin:$PATH
export HOST_UBUNTU=$(lsb_release -cs)
export PATH="${FLYNN_ROOT}/build/bin:/usr/local/go/bin:$PATH"
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
export JSON_FILE="${FLYNN_ROOT}/builder/manifest.json"
export UBUNTU_CODENAME
UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

echo "GO VERSION"
echo "$(go version)"

teardown_flynn() {
  echo "===> Stopping Flynn and removing install..."
  ./script/stop-all
  ./script/install-flynn --remove --clean --yes
}

# --- Phase: base (debootstrap + squashfs + manifest) ---
run_phase_base() {
  echo "===> [base] Stopping Flynn and cleaning install (before base image)..."
  teardown_flynn

  echo "===> [base] Preparing apt (IPv4) and base root image..."

  echo 'Acquire::ForceIPv4 "true";' | sudo tee /etc/apt/apt.conf.d/99force-ipv4

  CACHE_DIR=/var/cache/flynn/debootstrap
  ROOTFS=/var/lib/flynn/base-root

  if [ ! -f "${SQUASHFS}" ]; then
    mkdir -p "$ROOTFS"
    debootstrap \
      --variant=minbase \
      --include=squashfs-tools,curl,gnupg,ca-certificates,bash \
      --cache-dir="$CACHE_DIR" \
      "${UBUNTU_CODENAME}" \
      "$ROOTFS" \
      https://mirror.yuki.net.uk/ubuntu-ports/
    mksquashfs "$ROOTFS" "${SQUASHFS}" -noappend
  fi
  
  cd "${FLYNN_ROOT}"
  export SIZE
  SIZE=$(stat -c%s "${SQUASHFS}")
  export HASH
  HASH=$(./sha512_256_binary "${SQUASHFS}")

  echo "SIZE=${SIZE}"
  echo "HASH=${HASH}"

  jq --arg url "file://${SQUASHFS}" \
    --arg size "${SIZE}" \
    --arg hash "${HASH}" \
    '.base_layer.url = $url |
      .base_layer.size = ($size | tonumber) |
      .base_layer.hashes.sha512_256 = $hash' \
    "${JSON_FILE}" > "${JSON_FILE}.tmp" && mv "${JSON_FILE}.tmp" "${JSON_FILE}"

  echo "===> [base] Complete."
}

# --- Phase: cluster (Flynn teardown, build, start, flynn-builder) ---
# Set FLYNN_BUILD_SKIP_TEARDOWN=1 when chaining after teardown_flynn + run_phase_base (./build.sh all).
run_phase_cluster() {
  if [[ ! -f "${SQUASHFS}" ]]; then
    echo "ERROR: Missing base squashfs at ${SQUASHFS}" >&2
    echo "Run:  $0 base" >&2
    exit 1
  fi

  if [[ -z "${FLYNN_BUILD_SKIP_TEARDOWN:-}" ]]; then
    echo "===> [cluster] Stopping Flynn and cleaning install..."
    teardown_flynn
  else
    echo "===> [cluster] Skipping teardown (already done for this run)."
  fi

  echo 'Acquire::ForceIPv4 "true";' | sudo tee /etc/apt/apt.conf.d/99force-ipv4

  cd "${FLYNN_ROOT}" && \
    mkdir -p /etc/flynn && \
    mkdir -p /tmp/discoverd-data

  rm -rf /tmp/flynn-* && \
    rm -rf /var/log/flynn/* && \
    make clean && \
    bash ./host/apparmor/setup-apparmor.sh && \
    ./script/build-flynn --version "${VERSION}" && \
    rm -f build/bin/flynn-builder && \
    rm -f build/bin/flannel-wrapper && \
    go build -o build/bin/flannel-wrapper ./flannel/wrapper && \
    ./script/start-all && \
    zfs set sync=disabled flynn-default && \
    zfs set reservation=512M flynn-default && \
    zfs set refreservation=512M flynn-default

  MAX_RETRIES=10
  ATTEMPT=1

  while [[ ${ATTEMPT} -le ${MAX_RETRIES} ]]; do
    echo "===> Running flynn-builder build (attempt ${ATTEMPT} of ${MAX_RETRIES}) with version: ${VERSION}"
    if ./script/flynn-builder build --version="${VERSION}" --verbose; then
      echo "===> flynn-builder build succeeded!"
      break
    else
      echo ""
      echo "===> flynn-builder build FAILED (attempt ${ATTEMPT} of ${MAX_RETRIES})!"
      flynn-host ps -a
      if [[ ${ATTEMPT} -eq ${MAX_RETRIES} ]]; then
        echo "===> Maximum retry attempts reached. Exiting."
        exit 1
      fi
      echo "===> Retrying in 5 seconds..."
      sleep 5
      ATTEMPT=$((ATTEMPT + 1))
    fi
  done

  flynn-host ps -a

  cd "${FLYNN_ROOT}"
  cp ./script/install-flynn /usr/bin/install-flynn

  echo "===> [cluster] Stopping local Flynn stack after successful build..."
  ./script/stop-all

  echo "===> [cluster] Complete."
}

case "${PHASE}" in
  base)
    run_phase_base
    ;;
  cluster)
    run_phase_cluster
    ;;
  all)
    run_phase_base
    FLYNN_BUILD_SKIP_TEARDOWN=1 run_phase_cluster
    ;;
  *)
    echo "Internal error: unknown phase ${PHASE}" >&2
    exit 1
    ;;
esac

echo "===> Build complete!"
echo ""
echo "To create a release, commit your changes and run:"
echo "  ./script/release"

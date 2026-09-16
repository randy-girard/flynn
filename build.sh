#!/bin/bash
#
# Flynn Build Script
# Builds Flynn components
#
# Usage:
#   ./build.sh [OPTIONS] [PHASE]
#
# PHASE (default: all):
#   base         Build the debootstrap base root, squashfs, and refresh builder/manifest.json.
#                Slow; only re-run when changing Ubuntu series or base packages.
#   prep         Teardown (optional), clean workspace, Apparmor — ready for binaries/start.
#   binaries     Build host binaries via script/build-flynn (+ flannel-wrapper).
#   start        Start the local Flynn stack (required before flynn-builder).
#   toolchain    Build toolchain/base images (flynn-builder --only=toolchain).
#   apps         Build production app/CLI images (flynn-builder --only=apps).
#                Omits cluster-test images (test, test-apps, controller-examples).
#   test         Build cluster-test images (flynn-builder --only=test).
#   stop         Copy install-flynn and stop the local Flynn stack.
#   cluster      prep → binaries → start → toolchain → apps → stop.
#   all          Run base then cluster (same as the historical single-shot build).
#
# Local / Vagrant (builder VM):
#   Prefer the composed phases so the full pipeline still works in one command:
#     ./build.sh                         # base + cluster (first-time / full rebuild)
#     ./build.sh cluster                 # host binaries + toolchain + apps (base squashfs exists)
#     ./build.sh --version vYYYYMMDD.N cluster
#   Fine-grained phases match CI and can be run by hand for debugging; they must
#   stay in order (prep → binaries → start → toolchain → apps → stop) on the same
#   machine so /var/lib/flynn/layer-cache and build/images.json carry forward.
#
# Concurrency (optional):
#   TOOLCHAIN_CONCURRENCY  default 2 (conservative; toolchain images are heavy)
#   APPS_CONCURRENCY       default nproc locally, 2 on GitHub Actions, or FLYNN_BUILD_CONCURRENCY
#   GOMEMLIMIT             default 12GiB locally, 4GiB on GitHub Actions
#   FLYNN_BUILDER_MAX_RETRIES  default 10 locally, 4 on GitHub Actions
#   FLYNN_GO_BUILD_P       cap `go build -p` inside image jobs (default 2 on GHA)
#   CI sets APPS_CONCURRENCY via the release workflow input (default 2).
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
  base       Debootstrap + base squashfs + manifest (run rarely)
  prep       Teardown, clean, Apparmor
  binaries   Build host binaries (script/build-flynn)
  start      Start local Flynn stack
  toolchain  Build toolchain images (flynn-builder --only=toolchain)
  apps       Build production app/CLI images (omits test, test-apps, controller-examples)
  test       Build cluster-test images (test, test-apps, controller-examples)
  stop       Stop local Flynn stack after image builds
  cluster    prep → binaries → start → toolchain → apps → stop
  all        base then cluster

Examples:
  $0                                      # Vagrant/local: base + full cluster pipeline
  $0 cluster                              # Vagrant/local: binaries + toolchain + apps
  $0 base
  $0 --version v20240127.0 cluster
  $0 --version v20240127.0 binaries
USAGE
}

# Get the root directory of the Flynn project
FLYNN_ROOT="$(cd "$(dirname "$0")" && pwd)"
export FLYNN_ROOT
# shellcheck source=script/lib/apt-retry.sh
source "${FLYNN_ROOT}/script/lib/apt-retry.sh"

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
    base|prep|binaries|start|toolchain|apps|test|stop|cluster|all)
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

# Generate builder/manifest.json from the committed template
"${FLYNN_ROOT}/script/prepare-builder-manifest"

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
export UBUNTU_CODENAME
UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

echo "GO VERSION"
echo "$(go version)"

teardown_flynn() {
  if [[ -n "${FLYNN_BUILD_SKIP_TEARDOWN:-}" ]]; then
    echo "===> Skipping Flynn teardown (FLYNN_BUILD_SKIP_TEARDOWN set)"
    return 0
  fi
  echo "===> Stopping Flynn and removing install..."
  ./script/stop-all
  # install-flynn --remove does rm -rf /var/lib/flynn, which is also where the
  # base squashfs lives. Preserve it so `build.sh cluster` (documented as the
  # "base squashfs exists" path) does not delete its own prerequisite. Same
  # filesystem, so these are renames.
  local saved=""
  if [[ -f "${SQUASHFS}" ]]; then
    saved="/var/lib/flynn-base-layer.squashfs.keep"
    sudo mv -f "${SQUASHFS}" "${saved}"
  fi
  ./script/install-flynn --remove --clean --yes
  if [[ -n "${saved}" ]]; then
    sudo mkdir -p "$(dirname "${SQUASHFS}")"
    sudo mv -f "${saved}" "${SQUASHFS}"
    echo "===> Preserved base squashfs at ${SQUASHFS}"
  fi
}

require_base_squashfs() {
  if [[ ! -f "${SQUASHFS}" ]]; then
    echo "ERROR: Missing base squashfs at ${SQUASHFS}" >&2
    echo "Run:  $0 base" >&2
    exit 1
  fi
}

# --- Phase: base (debootstrap + squashfs + manifest) ---
run_phase_base() {
  echo "===> [base] Stopping Flynn and cleaning install (before base image)..."
  teardown_flynn

  echo "===> [base] Preparing apt (IPv4) and base root image..."

  echo 'Acquire::ForceIPv4 "true";' | sudo tee /etc/apt/apt.conf.d/99force-ipv4
  flynn_apt_install_conf

  CACHE_DIR=/var/cache/flynn/debootstrap
  ROOTFS=/var/lib/flynn/base-root

  # ubuntu-ports carries only non-amd64 arches (arm64, ppc64el, riscv64, s390x);
  # amd64 lives on the main archive. Pick the mirror to match the host arch so
  # debootstrap can find binary-${arch}/Packages.
  DEB_ARCH=$(dpkg --print-architecture)
  if [ "${DEB_ARCH}" = "amd64" ]; then
    DEB_MIRROR=http://archive.ubuntu.com/ubuntu/
  else
    DEB_MIRROR=https://mirror.yuki.net.uk/ubuntu-ports/
  fi

  if [ ! -f "${SQUASHFS}" ]; then
    mkdir -p "$ROOTFS" "$CACHE_DIR"
    debootstrap \
      --variant=minbase \
      --include=squashfs-tools,curl,gnupg,ca-certificates,bash \
      --cache-dir="$CACHE_DIR" \
      "${UBUNTU_CODENAME}" \
      "$ROOTFS" \
      "${DEB_MIRROR}"
    # Keep -comp/-Xcompression-level in sync with pkg/squashfs.
    mksquashfs "$ROOTFS" "${SQUASHFS}" -noappend \
      -comp zstd -Xcompression-level 15
  fi

  cd "${FLYNN_ROOT}"
  export SIZE
  SIZE=$(stat -c%s "${SQUASHFS}")
  export HASH
  HASH=$(./sha512_256_binary "${SQUASHFS}")

  echo "SIZE=${SIZE}"
  echo "HASH=${HASH}"

  "${FLYNN_ROOT}/script/prepare-builder-manifest"

  echo "===> [base] Complete."
}

# flynn-host EnableJobIsolation needs the ipset binary on this machine (builder
# VM or CI host). Cluster nodes get it from host/img/packages.sh; existing
# Vagrant builders may have been provisioned before setup.sh installed it.
ensure_builder_isolation_tools() {
  mkdir -p /tmp
  chmod 1777 /tmp 2>/dev/null || true
  if command -v ipset >/dev/null 2>&1; then
    return 0
  fi
  echo "===> installing ipset (required for flynn-host job isolation)"
  export DEBIAN_FRONTEND=noninteractive
  flynn_apt_install_conf
  if ! flynn_apt_cmd install -y ipset; then
    echo "ipset is required on the builder host (ConfigureNetworking/EnableJobIsolation)" >&2
    exit 1
  fi
}

# --- Phase: prep (teardown/clean before host binary build) ---
# Set FLYNN_BUILD_SKIP_TEARDOWN=1 when chaining after teardown_flynn + run_phase_base.
run_phase_prep() {
  require_base_squashfs

  if [[ -z "${FLYNN_BUILD_SKIP_TEARDOWN:-}" ]]; then
    echo "===> [prep] Stopping Flynn and cleaning install..."
    teardown_flynn
  else
    echo "===> [prep] Skipping teardown (already done for this run)."
  fi

  echo 'Acquire::ForceIPv4 "true";' | sudo tee /etc/apt/apt.conf.d/99force-ipv4
  flynn_apt_install_conf
  ensure_builder_isolation_tools

  cd "${FLYNN_ROOT}"
  mkdir -p /etc/flynn
  mkdir -p /tmp/discoverd-data

  rm -rf /var/log/flynn/* || true
  # Leftover flynn-builder job mounts only. Do not glob /tmp/flynn-* — that
  # deletes /tmp/flynn-build-attempt.log when smoke tees the full build there.
  rm -rf /tmp/flynn-build-mnt* || true
  make clean
  bash ./host/apparmor/setup-apparmor.sh

  echo "===> [prep] Complete."
}

# --- Phase: binaries (host binaries used to run the local cluster) ---
run_phase_binaries() {
  require_base_squashfs
  cd "${FLYNN_ROOT}"

  echo "===> [binaries] Building host binaries (script/build-flynn, omitting flynn-test)..."
  ./script/build-flynn --version "${VERSION}"

  # Force a fresh flynn-builder / flannel-wrapper for start-all. Rebuild here
  # with the host Go toolchain — do not delete flynn-builder and let
  # script/flynn-builder run builder/img/go.sh (host apt-get + a second Go
  # install) during toolchain.
  rm -f build/bin/flynn-builder
  rm -f build/bin/flannel-wrapper
  # Same VCS stamp skip as script/go-build-version: vboxsf git status is 128.
  GOFLAGS="-mod=vendor -buildvcs=false" go build -o build/bin/flannel-wrapper ./flannel/wrapper
  GOFLAGS="-mod=vendor -buildvcs=false" go build -o build/bin/flynn-builder ./builder

  echo "===> [binaries] Complete."
}

# --- Phase: start (local Flynn stack for flynn-builder jobs) ---
run_phase_start() {
  require_base_squashfs
  cd "${FLYNN_ROOT}"
  ensure_builder_isolation_tools

  echo "===> [start] Starting Flynn stack..."
  ./script/start-all
  zfs set sync=disabled flynn-default
  zfs set reservation=512M flynn-default
  zfs set refreservation=512M flynn-default

  echo "===> [start] Complete."
}

# Default image-build concurrency: keep toolchain conservative; let apps fan out
# to all CPUs on Vagrant/local unless CI (or the user) overrides.
default_apps_concurrency() {
  if [[ -n "${APPS_CONCURRENCY:-}" ]]; then
    echo "${APPS_CONCURRENCY}"
    return
  fi
  if [[ -n "${FLYNN_BUILD_CONCURRENCY:-}" ]]; then
    echo "${FLYNN_BUILD_CONCURRENCY}"
    return
  fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo 2
    return
  fi
  if command -v nproc >/dev/null 2>&1; then
    nproc
    return
  fi
  echo 4
}

default_gomemlimit() {
  if [[ -n "${GOMEMLIMIT:-}" ]]; then
    echo "${GOMEMLIMIT}"
    return
  fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "4GiB"
    return
  fi
  echo "12GiB"
}

default_builder_max_retries() {
  if [[ -n "${FLYNN_BUILDER_MAX_RETRIES:-}" ]]; then
    echo "${FLYNN_BUILDER_MAX_RETRIES}"
    return
  fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo 4
    return
  fi
  echo 10
}

# Run flynn-builder with retries for a single --only group.
run_flynn_builder_only() {
  local only="$1"
  local concurrency="$2"
  local max_retries
  max_retries="$(default_builder_max_retries)"
  local gomemlimit
  gomemlimit="$(default_gomemlimit)"
  local attempt=1

  cd "${FLYNN_ROOT}"
  while [[ ${attempt} -le ${max_retries} ]]; do
    echo "===> Running flynn-builder --only=${only} (attempt ${attempt} of ${max_retries}) version=${VERSION} concurrency=${concurrency} GOMEMLIMIT=${gomemlimit}"
    # flynn-builder has been observed at >20 GB RSS during --only=apps (9p
    # copies of layer diffs). Without a limit, concurrent mksquashfs then dies
    # with "Cannot allocate memory" even with -mem 1G per squash. Soft-cap the
    # Go heap so GC runs before the builder VM is exhausted. Hosted GitHub
    # runners (~16 GiB) need a lower cap so overlay go builds still have RAM.
    if FLYNN_BUILD_CONCURRENCY="${concurrency}" \
      GOMEMLIMIT="${gomemlimit}" \
      ./script/flynn-builder build --version="${VERSION}" --verbose --only="${only}"; then
      echo "===> flynn-builder --only=${only} succeeded!"
      return 0
    fi
    echo ""
    echo "===> flynn-builder --only=${only} FAILED (attempt ${attempt} of ${max_retries})!"
    flynn-host ps -a || true
    if [[ ${attempt} -eq ${max_retries} ]]; then
      echo "===> Maximum retry attempts reached. Exiting."
      return 1
    fi
    if [[ "${concurrency}" =~ ^[0-9]+$ ]] && [[ "${concurrency}" -gt 1 ]]; then
      concurrency=$(( (concurrency + 1) / 2 ))
      echo "===> Reducing concurrency to ${concurrency} after failed attempt (memory/IO pressure)"
    fi
    echo "===> Retrying in 5 seconds..."
    sleep 5
    attempt=$((attempt + 1))
  done
}

# --- Phase: toolchain (base/tool images) ---
run_phase_toolchain() {
  require_base_squashfs
  local concurrency="${TOOLCHAIN_CONCURRENCY:-2}"
  echo "===> [toolchain] Building toolchain images (concurrency=${concurrency})..."
  run_flynn_builder_only toolchain "${concurrency}"
  flynn-host ps -a || true
  echo "===> [toolchain] Complete."
}

# --- Phase: apps (production images; omits cluster-test images) ---
run_phase_apps() {
  require_base_squashfs
  local concurrency
  concurrency="$(default_apps_concurrency)"
  echo "===> [apps] Building production app images (concurrency=${concurrency})..."
  run_flynn_builder_only apps "${concurrency}"
  flynn-host ps -a || true
  echo "===> [apps] Complete."
}

# --- Phase: test (cluster-integration images used by test/) ---
run_phase_test() {
  require_base_squashfs
  local concurrency
  concurrency="$(default_apps_concurrency)"
  echo "===> [test] Building cluster-test images (concurrency=${concurrency})..."
  run_flynn_builder_only test "${concurrency}"
  flynn-host ps -a || true
  echo "===> [test] Complete."
}

# --- Phase: stop (tear down local stack after successful image builds) ---
run_phase_stop() {
  cd "${FLYNN_ROOT}"
  cp ./script/install-flynn /usr/bin/install-flynn

  echo "===> [stop] Stopping local Flynn stack..."
  ./script/stop-all

  echo "===> [stop] Complete."
}

# --- Phase: cluster (full cluster image pipeline) ---
run_phase_cluster() {
  run_phase_prep
  run_phase_binaries
  run_phase_start
  run_phase_toolchain
  run_phase_apps
  run_phase_stop
  echo "===> [cluster] Complete."
}

case "${PHASE}" in
  base)
    run_phase_base
    ;;
  prep)
    run_phase_prep
    ;;
  binaries)
    run_phase_binaries
    ;;
  start)
    run_phase_start
    ;;
  toolchain)
    run_phase_toolchain
    ;;
  apps)
    run_phase_apps
    ;;
  test)
    run_phase_test
    ;;
  stop)
    run_phase_stop
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
exit 0
echo ""
echo "To create a release, commit your changes and run:"
echo "  ./script/release"

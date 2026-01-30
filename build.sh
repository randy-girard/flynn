#!/bin/bash
#
# Flynn Build Script
# Builds Flynn components
#
# Usage:
#   ./build.sh                              # Build
#   ./build.sh --version v20240127.0        # Build with specific version
#
# For GitHub Releases, run ./script/github-release after committing your changes.
#

set -eo pipefail

# Parse command line arguments
VERSION=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      VERSION="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [OPTIONS]"
      echo ""
      echo "OPTIONS:"
      echo "  --version VERSION          Version for build (e.g., v20240127)"
      echo ""
      echo "For GitHub Releases, run ./script/github-release after committing."
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

# Export FLYNN_VERSION so it's available to all subprocesses
export FLYNN_VERSION="${VERSION}"

export PATH=/usr/local/go/bin:$PATH
export HOST_UBUNTU=$(lsb_release -cs)
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
zfs set refreservation=512M flynn-default

# Flynn builder step with retry loop
while true; do
  echo "===> Running flynn-builder build with version: ${VERSION}"
  if ./script/flynn-builder build --version="${VERSION}" --verbose; then
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

flynn-host ps -a

cd /root/go/src/github.com/flynn/flynn
cp ./script/install-flynn /usr/bin/install-flynn

echo "===> Build complete!"
echo ""
echo "To create a GitHub release, commit your changes and run:"
echo "  ./script/github-release"
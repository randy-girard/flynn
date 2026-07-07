package main

import (
	"encoding/base64"
	"fmt"
)

// buildctlDaemonlessScript is vendored from moby/buildkit:v0.23.2 because release
// tarballs no longer ship buildctl-daemonless.sh.
var buildctlDaemonlessScript = `#!/bin/sh
# buildctl-daemonless.sh spawns ephemeral buildkitd for executing buildctl.
#
# Usage: buildctl-daemonless.sh build ...
#
# Flags for buildkitd can be specified as $BUILDKITD_FLAGS .
#
# The script is compatible with BusyBox shell.
set -eu

: ${BUILDCTL=buildctl}
: ${BUILDCTL_CONNECT_RETRIES_MAX=10}
: ${BUILDKITD=buildkitd}
: ${BUILDKITD_FLAGS=}
: ${ROOTLESSKIT=rootlesskit}

# $tmp holds the following files:
# * pid
# * addr
# * log
tmp=$(mktemp -d /tmp/buildctl-daemonless.XXXXXX)
trap "kill \$(cat $tmp/pid) || true; wait \$(cat $tmp/pid) || true; rm -rf $tmp" EXIT

startBuildkitd() {
    addr=
    helper=
    if [ $(id -u) = 0 ]; then
        addr=unix:///run/buildkit/buildkitd.sock
    else
        addr=unix://$XDG_RUNTIME_DIR/buildkit/buildkitd.sock
        helper=$ROOTLESSKIT
    fi
    $helper $BUILDKITD $BUILDKITD_FLAGS --addr=$addr >$tmp/log 2>&1 &
    pid=$!
    echo $pid >$tmp/pid
    echo $addr >$tmp/addr
}

# buildkitd supports NOTIFY_SOCKET but as far as we know, there is no easy way
# to wait for NOTIFY_SOCKET activation using busybox-builtin commands...
waitForBuildkitd() {
    addr=$(cat $tmp/addr)
    try=0
    max=$BUILDCTL_CONNECT_RETRIES_MAX
    until $BUILDCTL --addr=$addr debug workers >/dev/null 2>&1; do
        if [ $try -gt $max ]; then
            echo >&2 "could not connect to $addr after $max trials"
            echo >&2 "========== log =========="
            cat >&2 $tmp/log
            exit 1
        fi
        sleep $(awk "BEGIN{print (100 + $try * 20) * 0.001}")
        try=$(expr $try + 1)
    done
}

startBuildkitd
waitForBuildkitd
$BUILDCTL --addr=$(cat $tmp/addr) "$@"
`

func dockerbuilderJobArgs() []string {
	scriptB64 := base64.StdEncoding.EncodeToString([]byte(buildctlDaemonlessScript))
	return []string{"bash", "-c", fmt.Sprintf(`set -euo pipefail
arch=$(uname -m)
case "$arch" in
  aarch64|arm64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
ver=v0.23.2
buildkit_root=/tmp/buildkit
if ! "${buildkit_root}/bin/buildctl" --version >/dev/null 2>&1; then
  printf '\e[1G-----> Installing BuildKit...\n'
  mkdir -p "${buildkit_root}"
  curl -fsSL "https://github.com/moby/buildkit/releases/download/${ver}/buildkit-${ver}.linux-${arch}.tar.gz" \
    | tar -xzf - -C "${buildkit_root}"
fi
mkdir -p "${buildkit_root}/bin"
base64 -d > "${buildkit_root}/bin/buildctl-daemonless.sh" <<'FLYNN_BUILDCTL_SCRIPT_B64_EOF'
%s
FLYNN_BUILDCTL_SCRIPT_B64_EOF
chmod +x "${buildkit_root}/bin/buildctl-daemonless.sh"
ln -sf "${buildkit_root}/bin/buildctl-daemonless.sh" /usr/local/bin/buildctl-daemonless.sh
ln -sf "${buildkit_root}/bin/buildctl" /usr/local/bin/buildctl
ln -sf "${buildkit_root}/bin/buildkitd" /usr/local/bin/buildkitd
export PATH="${buildkit_root}/bin:/usr/local/bin:${PATH}"
exec bash /builder/build.sh`, scriptB64)}
}

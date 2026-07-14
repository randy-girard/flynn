#!/bin/bash
set -eo pipefail

export TMPDIR="${TMPDIR:-/tmp}"

app_dir="${TMPDIR}/app"
dockerfile="${DOCKERFILE:-Dockerfile}"

mkdir -p "${app_dir}"
cd "${app_dir}"

echo $'\e[1G----->' Extracting source...
cat | tar -xm

if [[ ! -f "${dockerfile}" ]]; then
  echo $'\e[1G----->' "No ${dockerfile} found in repository"
  exit 1
fi

if [[ -n "${CONTROLLER_KEY}" ]]; then
  mkdir -p /run/secrets
  echo "${CONTROLLER_KEY}" > /run/secrets/controller_key
  chmod 600 /run/secrets/controller_key
  unset CONTROLLER_KEY
fi

ensure_buildkit() {
  export PATH="/usr/local/buildkit/bin:/usr/local/bin:${PATH}"
  if command -v buildctl >/dev/null && buildctl --version >/dev/null 2>&1 \
    && { [[ -x /usr/local/bin/buildctl-daemonless.sh ]] || [[ -x /builder/buildctl-daemonless.sh ]]; }; then
    return 0
  fi
  local arch
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *)
      echo $'\e[1G----->' "unsupported architecture: $(uname -m)" >&2
      return 1
      ;;
  esac
  local ver=v0.23.2
  echo $'\e[1G----->' Installing BuildKit...
  mkdir -p /usr/local/buildkit
  curl -fsSL "https://github.com/moby/buildkit/releases/download/${ver}/buildkit-${ver}.linux-${arch}.tar.gz" \
    | tar -xzf - -C /usr/local/buildkit
  if [[ -x /builder/buildctl-daemonless.sh ]]; then
    install -m 0755 /builder/buildctl-daemonless.sh /usr/local/buildkit/bin/buildctl-daemonless.sh
  fi
  ln -sf /usr/local/buildkit/bin/buildctl-daemonless.sh /usr/local/bin/buildctl-daemonless.sh
  ln -sf /usr/local/buildkit/bin/buildctl /usr/local/bin/buildctl
  ln -sf /usr/local/buildkit/bin/buildkitd /usr/local/bin/buildkitd
}

image_tar="${TMPDIR}/image.tar"
echo $'\e[1G----->' Building Docker image...
ensure_buildkit
export PATH="/usr/local/buildkit/bin:/usr/local/bin:${PATH}"
mkdir -p /run/buildkit /tmp/buildkitd
export BUILDKITD_FLAGS="${BUILDKITD_FLAGS:---root=/tmp/buildkitd --oci-worker-snapshotter=native}"
export CI="${CI:-true}"
export BUILDKIT_PROGRESS="${BUILDKIT_PROGRESS:-plain}"

buildctl_cmd=(buildctl-daemonless.sh)
if command -v stdbuf >/dev/null 2>&1; then
  buildctl_cmd=(stdbuf -oL -eL buildctl-daemonless.sh)
fi
"${buildctl_cmd[@]}" build \
  --frontend dockerfile.v0 \
  --local context="${app_dir}" \
  --local dockerfile="${app_dir}" \
  --opt "filename=${dockerfile}" \
  --output "type=docker,dest=${image_tar}" \
  --progress=plain 2>&1

echo $'\e[1G----->' Uploading image...
/bin/create-artifact --tar "${image_tar}"

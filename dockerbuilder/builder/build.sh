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
cache_in="${TMPDIR}/buildkit-cache"
cache_out="${TMPDIR}/buildkit-cache-out"
echo $'\e[1G----->' Building Docker image...
ensure_buildkit
export PATH="/usr/local/buildkit/bin:/usr/local/bin:${PATH}"
mkdir -p /run/buildkit /tmp/buildkitd "${cache_in}"
export BUILDKITD_FLAGS="${BUILDKITD_FLAGS:---root=/tmp/buildkitd --oci-worker-snapshotter=native}"
export CI="${CI:-true}"
export BUILDKIT_PROGRESS="${BUILDKIT_PROGRESS:-plain}"

if [[ -n "${BUILD_CACHE_URL}" ]]; then
  echo $'\e[1G----->' Restoring Docker build cache...
  curl --fail --silent --retry 3 --location "${BUILD_CACHE_URL}" \
    | tar --extract --gunzip --directory "${cache_in}" &>/dev/null || true
fi

buildctl_cmd=(buildctl-daemonless.sh)
if command -v stdbuf >/dev/null 2>&1; then
  buildctl_cmd=(stdbuf -oL -eL buildctl-daemonless.sh)
fi
build_args=(
  build
  --frontend dockerfile.v0
  --local "context=${app_dir}"
  --local "dockerfile=${app_dir}"
  --opt "filename=${dockerfile}"
  --output "type=docker,dest=${image_tar}"
  --progress=plain
  --export-cache "type=local,dest=${cache_out},mode=max"
)
if [[ -n "$(ls -A "${cache_in}" 2>/dev/null)" ]]; then
  build_args+=(--import-cache "type=local,src=${cache_in}")
fi
"${buildctl_cmd[@]}" "${build_args[@]}" 2>&1

cache_pid=
if [[ -n "${BUILD_CACHE_URL}" ]]; then
  save_dir="${cache_out}"
  if [[ ! -d "${save_dir}" ]] || [[ -z "$(ls -A "${save_dir}" 2>/dev/null)" ]]; then
    save_dir="${cache_in}"
  fi
  compress=gzip
  if command -v pigz >/dev/null 2>&1; then
    compress=pigz
  fi
  (
    echo $'\e[1G----->' Saving Docker build cache...
    tar --create --directory "${save_dir}" --use-compress-program="${compress}" . \
      | curl --fail --silent --retry 3 --output "$(mktemp)" --request PUT --upload-file - "${BUILD_CACHE_URL}" \
      || echo $'\e[1G----->' "WARN: failed to save Docker build cache"
  ) &
  cache_pid=$!
fi

echo $'\e[1G----->' Uploading image...
/bin/create-artifact --tar "${image_tar}"
if [[ -n "${cache_pid}" ]]; then
  wait "${cache_pid}" || true
fi

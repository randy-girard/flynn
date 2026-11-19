#!/bin/bash
set -eo pipefail

export TMPDIR="${TMPDIR:-/tmp}"

# GitHub Releases digest for buildkit-v0.23.2.linux-*.tar.gz
# (api.github.com/repos/moby/buildkit/releases/tags/v0.23.2).
# The dockerbuilder image already bakes BuildKit in img/packages.sh; this
# fallback is only for older/custom images that lack it.
BUILDKIT_VERSION=v0.23.2
BUILDKIT_SHA256_AMD64=2771c3403e3a1f75a83cde387a05365794d3b900c355e864772a36c3ce541f82
BUILDKIT_SHA256_ARM64=6385ff70b2fb4134b50ac3183eea3a0b06c6f6129173940d73178ae0477368f1

ensure_buildkit() {
  export PATH="/usr/local/buildkit/bin:/usr/local/bin:${PATH}"
  if command -v buildctl >/dev/null && buildctl --version >/dev/null 2>&1 \
    && { [[ -x /usr/local/bin/buildctl-daemonless.sh ]] || [[ -x /builder/buildctl-daemonless.sh ]]; }; then
    return 0
  fi
  local arch sha256
  case "$(uname -m)" in
    x86_64|amd64)
      arch=amd64
      sha256="${BUILDKIT_SHA256_AMD64}"
      ;;
    aarch64|arm64)
      arch=arm64
      sha256="${BUILDKIT_SHA256_ARM64}"
      ;;
    *)
      echo $'\e[1G----->' "unsupported architecture: $(uname -m)" >&2
      return 1
      ;;
  esac
  echo $'\e[1G----->' Installing BuildKit...
  mkdir -p /usr/local/buildkit
  local tgz
  tgz="$(mktemp)"
  curl -fsSL "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-${arch}.tar.gz" \
    -o "${tgz}"
  echo "${sha256}  ${tgz}" | sha256sum -c -
  tar -xzf "${tgz}" -C /usr/local/buildkit
  rm -f "${tgz}"
  if [[ -x /builder/buildctl-daemonless.sh ]]; then
    install -m 0755 /builder/buildctl-daemonless.sh /usr/local/buildkit/bin/buildctl-daemonless.sh
  fi
  ln -sf /usr/local/buildkit/bin/buildctl-daemonless.sh /usr/local/bin/buildctl-daemonless.sh
  ln -sf /usr/local/buildkit/bin/buildctl /usr/local/bin/buildctl
  ln -sf /usr/local/buildkit/bin/buildkitd /usr/local/bin/buildkitd
}

# path_inside fails when a Dockerfile symlink resolves outside the repo.
path_inside() {
  local root="$1" file="$2" resolved root_resolved
  resolved="$(readlink -f "${file}")"
  root_resolved="$(readlink -f "${root}")"
  case "${resolved}" in
    "${root_resolved}"|"${root_resolved}"/*) return 0 ;;
    *) return 1 ;;
  esac
}

# read_build_spec parses the line protocol. Build-arg values stay in the
# arrays and are passed to BuildKit. They are not printed.
read_build_spec() {
  SPEC_ID=()
  SPEC_CTX=()
  SPEC_FILE=()
  SPEC_TARGET=()
  SPEC_ARG_KEYS=()
  SPEC_ARG_VALS=()
  SPEC_COUNT=0
  local rest count i id ctx file target nargs j k v
  rest="$1"
  count="${rest%%$'\n'*}"
  rest="${rest#*$'\n'}"
  SPEC_COUNT="${count}"
  i=0
  while [[ "${i}" -lt "${count}" ]]; do
    id="${rest%%$'\n'*}"
    rest="${rest#*$'\n'}"
    ctx="${rest%%$'\n'*}"
    rest="${rest#*$'\n'}"
    file="${rest%%$'\n'*}"
    rest="${rest#*$'\n'}"
    target="${rest%%$'\n'*}"
    rest="${rest#*$'\n'}"
    nargs="${rest%%$'\n'*}"
    rest="${rest#*$'\n'}"
    SPEC_ID+=("${id}")
    SPEC_CTX+=("${ctx}")
    SPEC_FILE+=("${file}")
    SPEC_TARGET+=("${target}")
    j=0
    while [[ "${j}" -lt "${nargs}" ]]; do
      k="${rest%%$'\n'*}"
      rest="${rest#*$'\n'}"
      v="${rest%%$'\n'*}"
      rest="${rest#*$'\n'}"
      if [[ "${i}" -eq 0 ]]; then
        SPEC_ARG_KEYS+=("${k}")
        SPEC_ARG_VALS+=("${v}")
      fi
      j=$((j + 1))
    done
    i=$((i + 1))
  done
}

rotate_cache() {
  if [[ -d "${cache_out}" ]] && [[ -n "$(ls -A "${cache_out}" 2>/dev/null)" ]]; then
    rm -rf "${cache_in}"
    mv "${cache_out}" "${cache_in}"
  fi
  mkdir -p "${cache_out}"
}

build_one() {
  local context_dir="$1" dockerfile_dir="$2" filename="$3" target="$4" artifact_id="$5"
  local dockerfile_path="${dockerfile_dir}/${filename}"
  if [[ ! -f "${dockerfile_path}" ]]; then
    echo $'\e[1G----->' "No ${filename} found in repository"
    exit 1
  fi
  if ! path_inside "${app_dir}" "${dockerfile_path}"; then
    echo $'\e[1G----->' "Dockerfile ${filename} escapes the repository"
    exit 1
  fi
  echo $'\e[1G----->' Building Docker image...
  local -a args
  args=(
    build
    --frontend dockerfile.v0
    --local "context=${context_dir}"
    --local "dockerfile=${dockerfile_dir}"
    --opt "filename=${filename}"
    --output "type=docker,dest=${image_tar}"
    --progress=plain
    --export-cache "type=local,dest=${cache_out},mode=max"
  )
  if [[ -n "${target}" && "${target}" != "-" ]]; then
    args+=(--opt "target=${target}")
  fi
  local i=0
  while [[ "${i}" -lt "${#SPEC_ARG_KEYS[@]}" ]]; do
    args+=(--opt "build-arg:${SPEC_ARG_KEYS[${i}]}=${SPEC_ARG_VALS[${i}]}")
    i=$((i + 1))
  done
  if [[ -n "$(ls -A "${cache_in}" 2>/dev/null)" ]]; then
    args+=(--import-cache "type=local,src=${cache_in}")
  fi
  "${buildctl_cmd[@]}" "${args[@]}" 2>&1
  echo $'\e[1G----->' Uploading image...
  IMAGE_ARTIFACT_ID="${artifact_id}" /bin/create-artifact --tar "${image_tar}"
  rm -f "${image_tar}"
  rotate_cache
}

save_build_cache() {
  if [[ -z "${BUILD_CACHE_PUT_URL}" ]]; then
    return 0
  fi
  local save_dir="${cache_in}"
  if [[ ! -d "${save_dir}" ]] || [[ -z "$(ls -A "${save_dir}" 2>/dev/null)" ]]; then
    save_dir="${cache_out}"
  fi
  if [[ ! -d "${save_dir}" ]] || [[ -z "$(ls -A "${save_dir}" 2>/dev/null)" ]]; then
    return 0
  fi
  local compress=gzip
  if command -v pigz >/dev/null 2>&1; then
    compress=pigz
  fi
  (
    echo $'\e[1G----->' Saving Docker build cache...
    tar --create --directory "${save_dir}" --use-compress-program="${compress}" . \
      | curl --fail --silent --retry 3 --output "$(mktemp)" --request PUT --upload-file - "${BUILD_CACHE_PUT_URL}" \
      || echo $'\e[1G----->' "WARN: failed to save Docker build cache"
  ) &
  wait "$!" || true
}

main() {
  app_dir="${TMPDIR}/app"
  image_tar="${TMPDIR}/image.tar"
  cache_in="${TMPDIR}/buildkit-cache"
  cache_out="${TMPDIR}/buildkit-cache-out"
  SPEC_ARG_KEYS=()
  SPEC_ARG_VALS=()

  mkdir -p "${app_dir}"
  cd "${app_dir}"

  echo $'\e[1G----->' Extracting source...
  cat | tar -xm

  if [[ -z "${FLYNN_BUILD_SPEC}" ]]; then
    local dockerfile="${DOCKERFILE:-Dockerfile}"
    if [[ ! -f "${dockerfile}" ]]; then
      echo $'\e[1G----->' "No ${dockerfile} found in repository"
      exit 1
    fi
  fi

  if [[ -n "${CONTROLLER_KEY}" ]]; then
    mkdir -p /run/secrets
    echo "${CONTROLLER_KEY}" > /run/secrets/controller_key
    chmod 600 /run/secrets/controller_key
    unset CONTROLLER_KEY
  fi

  ensure_buildkit
  export PATH="/usr/local/buildkit/bin:/usr/local/bin:${PATH}"
  mkdir -p /run/buildkit /tmp/buildkitd "${cache_in}" "${cache_out}"
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

  if [[ -n "${FLYNN_BUILD_SPEC}" ]]; then
    read_build_spec "${FLYNN_BUILD_SPEC}"
    local i=0
    while [[ "${i}" -lt "${SPEC_COUNT}" ]]; do
      local ctx="${SPEC_CTX[${i}]}"
      local context_dir="${app_dir}"
      if [[ "${ctx}" != "." && -n "${ctx}" ]]; then
        case "${ctx}" in
          /*|..|../*|*/..|*/../*)
            echo $'\e[1G----->' "Dockerfile context escapes the repository"
            exit 1
            ;;
        esac
        context_dir="${app_dir}/${ctx}"
      fi
      build_one "${context_dir}" "${context_dir}" "${SPEC_FILE[${i}]}" "${SPEC_TARGET[${i}]}" "${SPEC_ID[${i}]}"
      i=$((i + 1))
    done
  else
    local dockerfile="${DOCKERFILE:-Dockerfile}"
    build_one "${app_dir}" "${app_dir}" "${dockerfile}" "-" "${IMAGE_ARTIFACT_ID}"
  fi

  save_build_cache
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

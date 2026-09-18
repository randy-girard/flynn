#!/bin/bash
set -eo pipefail

export TMPDIR="${TMPDIR:-"/tmp"}"

app_dir="${TMPDIR}/app"
env_dir="${TMPDIR}/env"
build_root="${TMPDIR}/build"
build_dir="${build_root}/app"
cache_root="${TMPDIR}/cache"
buildpack_root="/builder/buildpacks"
env_cookie=.ENV_DIR_bdca46b87df0537eaefe79bb632d37709ff1df18

mkdir -p ${app_dir}
mkdir -p ${cache_root}
mkdir -p ${buildpack_root}
mkdir -p ${build_dir}/.profile.d

# create the "flynn" user
source "/builder/create-user.sh"

echo_title() {
  echo $'\e[1G----->' $*
}

echo_normal() {
  echo $'\e[1G      ' $*
}

ensure_indent() {
  while read line; do
    if [[ "${line}" == --* ]]; then
      echo $'\e[1G'${line}
    else
      echo $'\e[1G      ' "${line}"
    fi
  done
}

run_unprivileged() {
  setuidgid "${USER}" $@
}

# run curl silently and retry upto 3 times
curl() {
  $(which curl) --fail --silent --retry 3 $@
}

# removes leading and trailing whitespace
trim() {
  local var="$*"
  var="${var#"${var%%[![:space:]]*}"}"
  var="${var%"${var##*[![:space:]]}"}"
  echo -n "$var"
}

prune_slugignore() {
  shopt -s nullglob
  # read slugignore into array
  local globs=()
  local paths=()
  readarray -t globs < "${build_dir}/.slugignore"
  # for line in slugignore
  for glob in ${globs[@]}; do
    # strip whitespace
    glob=$(trim ${glob})
    # ignore blank lines and comment lines
    if [[ ${glob} == "" ]] || [[ ${glob:0:1} == "#" ]]; then
      continue
    fi
    # remove leading slash(es)
    glob="${glob#"${glob%%[!"/"]*}"}"
    # append to build root and add to array of paths to remove
    paths=("${paths[@]}" ${build_dir}/${glob})
  done
  echo_title "Deleting ${#paths[@]} files matching .slugignore patterns."
  rm -rf ${paths[@]}
  shopt -u nullglob
}

cd ${app_dir}

## Load source from STDIN
cat | tar -xm

if [[ -d "${env_cookie}" ]]; then
  mv "${env_cookie}" "${env_dir}"
  envdir="true"
fi

if [[ -n "${BUILD_CACHE_URL}" ]]; then
  curl --location "${BUILD_CACHE_URL}" | tar --extract --gunzip --directory "${cache_root}" &>/dev/null || true
fi

# In heroku, there are two separate directories, and some
# buildpacks expect that.
cp -r . ${build_dir}
ln -s "${app_dir}" "/app"
chown -R "${USER}:${USER}" \
  "${TMPDIR}" \
  "${app_dir}" \
  "${build_dir}" \
  "${cache_root}"

## Protect CONTROLLER_KEY from buildpack code
# Save the controller key to a root-only file so that /bin/create-artifact
# (which runs as root) can still read it, but buildpack code (which runs as
# the unprivileged "flynn" user) cannot access it.
if [[ -n "${CONTROLLER_KEY}" ]]; then
  mkdir -p /run/secrets
  echo "${CONTROLLER_KEY}" > /run/secrets/controller_key
  chmod 600 /run/secrets/controller_key
  unset CONTROLLER_KEY
fi

## Buildpack fixes

export APP_DIR="${app_dir}"
export HOME="${app_dir}"
export REQUEST_ID="flynn-build"

# SEC-010: Save SSH credentials to files and unset from environment.
# The key is accessible during build for git operations, then removed
# after compilation to limit the exposure window.
ssh_cleanup_needed=false
if [[ -n "${SSH_CLIENT_KEY}" ]]; then
  mkdir -p ${HOME}/.ssh
  file="${HOME}/.ssh/id_rsa"
  echo "${SSH_CLIENT_KEY}" > ${file}
  chown -R "${USER}:${USER}" ${HOME}/.ssh
  chmod 600 ${file}
  unset SSH_CLIENT_KEY
  ssh_cleanup_needed=true
fi

if [[ -n "${SSH_CLIENT_HOSTS}" ]]; then
  mkdir -p ${HOME}/.ssh
  file="${HOME}/.ssh/known_hosts"
  echo "${SSH_CLIENT_HOSTS}" > ${file}
  chown -R "${USER}:${USER}" ${HOME}/.ssh
  chmod 600 ${file}
  unset SSH_CLIENT_HOSTS
fi

# Fix for https://github.com/flynn/flynn/issues/85
export CURL_CONNECT_TIMEOUT=30

# Bump max time to download a single runtime tarball from its default of
# 30s (only makes sense on EC2) to 10 minutes
export CURL_TIMEOUT=600

# Remove files matched by .slugignore
if [[ -f "${build_dir}/.slugignore" ]]; then
  prune_slugignore
fi

## Buildpack detection

# Ordering here is in line number order from buildpacks.txt
buildpacks=(${buildpack_root}/*)
selected_buildpack=

try_inline_buildpack() {
  local detect="${build_dir}/bin/detect"
  local compile="${build_dir}/bin/compile"
  if [[ ! -f "${detect}" || ! -f "${compile}" ]]; then
    return 1
  fi
  chmod +x "${detect}" "${compile}" "${build_dir}/bin/release" 2>/dev/null || true
  local name
  if name=$(run_unprivileged "${detect}" "${build_dir}" 2>/dev/null); then
    selected_buildpack="${build_dir}"
    buildpack_name="${name:-Inline}"
    return 0
  fi
  return 1
}

try_buildpacks_file() {
  local list="${build_dir}/.buildpacks"
  if [[ ! -f "${list}" ]]; then
    return 1
  fi
  local url
  url=$(grep -v '^[[:space:]]*#' "${list}" | grep -v '^[[:space:]]*$' | head -n1)
  url=$(trim "${url}")
  if [[ -z "${url}" ]]; then
    return 1
  fi
  echo_title "Fetching buildpack from .buildpacks"
  rm -rf "${buildpack_root}"/custom_*
  bash /builder/install-buildpack \
    "${buildpack_root}" \
    "${url}" \
    custom \
    "${env_dir}"
  local pack
  pack=$(echo "${buildpack_root}"/custom_*)
  if [[ ! -d "${pack}" ]]; then
    return 1
  fi
  chmod -R a+rX "${pack}"
  find "${pack}/bin" -type f -exec chmod a+x {} + 2>/dev/null || true
  chown -R "${USER}:${USER}" "${pack}"
  local name
  if name=$(run_unprivileged "${pack}/bin/detect" "${build_dir}"); then
    selected_buildpack="${pack}"
    buildpack_name="${name}"
    buildpacks=("${pack}")
    return 0
  fi
  return 1
}

if [[ -n "${BUILDPACK_URL}" ]]; then
  echo_title "Fetching custom buildpack"

  # Clone as root: setuidgid exec of /builder/install-buildpack exits 111 when
  # the script is missing +x or the flynn user cannot exec it. Do not swallow
  # git clone errors — they are the only signal when GitHub/git fails.
  rm -rf "${buildpack_root}"/custom_*
  bash /builder/install-buildpack \
    "${buildpack_root}" \
    "${BUILDPACK_URL}" \
    custom \
    "${env_dir}"
  buildpacks=("${buildpack_root}"/custom_*)
  selected_buildpack="${buildpacks[0]}"
  if [[ ! -d "${selected_buildpack}" ]]; then
    echo_title "Unable to fetch custom buildpack"
    exit 1
  fi
  chmod -R a+rX "${selected_buildpack}"
  find "${selected_buildpack}/bin" -type f -exec chmod a+x {} + 2>/dev/null || true
  chown -R "${USER}:${USER}" "${selected_buildpack}"
  buildpack_name=$(run_unprivileged "${selected_buildpack}/bin/detect" "${build_dir}")
else
  for buildpack in "${buildpacks[@]}"; do
    buildpack_name=$(run_unprivileged ${buildpack}/bin/detect "${build_dir}" 2>/dev/null) \
      && selected_buildpack="${buildpack}" \
      && break
  done
fi

if [[ -z "${selected_buildpack}" ]]; then
  try_inline_buildpack || true
fi
if [[ -z "${selected_buildpack}" ]]; then
  try_buildpacks_file || true
fi

if [[ -n "${selected_buildpack}" ]]; then
  echo_title "${buildpack_name} app detected"
else
  echo_title "Unable to select a buildpack"
  echo_normal "No bundled buildpack matched this app."
  echo_normal "Add language files (requirements.txt, Gemfile, package.json, …),"
  echo_normal "a .buildpacks URL, BUILDPACK_URL, or bin/detect + bin/compile"
  echo_normal "for an inline buildpack. For a Dockerfile: flynn stack:set container"
  exit 1
fi

## Buildpack compile
if [[ -n "${envdir}" ]]; then
  run_unprivileged ${selected_buildpack}/bin/compile \
    "${build_dir}" \
    "${cache_root}" \
    "${env_dir}" \
    | ensure_indent
else
  run_unprivileged ${selected_buildpack}/bin/compile \
    "${build_dir}" \
    "${cache_root}" \
    | ensure_indent
fi

run_unprivileged ${selected_buildpack}/bin/release \
  "${build_dir}" \
  "${cache_root}" \
  > ${build_dir}/.release

# SEC-010: Remove SSH key material after buildpack compilation is complete.
# This limits the window during which the key is accessible on disk.
if [[ "${ssh_cleanup_needed}" == "true" ]]; then
  rm -f ${HOME}/.ssh/id_rsa
  rm -f ${HOME}/.ssh/known_hosts
  ssh_cleanup_needed=false
fi

## Display process types

echo_title "Discovering process types"
if [[ -f "${build_dir}/Procfile" ]]; then
  types=$(ruby -r yaml -e "puts YAML.load_file('${build_dir}/Procfile').keys.join(', ')")
  echo_normal "Procfile declares types -> ${types}"
fi
default_types=""
if [[ -s "${build_dir}/.release" ]]; then
  default_types=$(ruby -r yaml -e "puts (YAML.load_file('${build_dir}/.release') || {}).fetch('default_process_types', {}).keys.join(', ')")
  if [[ -n "${default_types}" ]]; then
    echo_normal "Default process types for ${buildpack_name} -> ${default_types}"
  fi
fi

# ensure all app files are owned by USER
chown -R "${USER}:${USER}" "${build_dir}"

# import user information
mkdir -p "${build_root}/etc"
cp "/etc/passwd" "${build_root}/etc/passwd"
cp "/etc/group" "${build_root}/etc/group"

## Produce slug
/bin/create-artifact \
  --dir "${build_root}" \
  --uid "${USER_UID}" \
  --gid "${USER_GID}" \
  | ensure_indent

if [[ -n "${BUILD_CACHE_URL}" ]]; then
  tar \
    --create \
    --directory "${cache_root}" \
    --use-compress-program=pigz \
    . \
  | curl \
    --output "$(mktemp)" \
    --request PUT \
    --upload-file - \
    "${BUILD_CACHE_URL}"
fi

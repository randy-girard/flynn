#!/usr/bin/env bats

load "helper"

@test "GitHub unit tests only start PostgreSQL for Flynn appliances" {
  wf="${ROOT}/.github/workflows/unit-tests.yml"
  dockerfile="${ROOT}/script/docker/unit-tests/Dockerfile"
  entrypoint="${ROOT}/script/docker/unit-tests/entrypoint.sh"
  grep -q 'postgresql' "${wf}"
  grep -q 'Start PostgreSQL' "${wf}"
  for f in "${wf}" "${dockerfile}" "${entrypoint}"; do
    if grep -E 'mariadb-server|mariadb-backup|redis-server|mongodb-org' "${f}"; then
      echo "${f} must not install extracted plugin daemons" >&2
      return 1
    fi
  done
  if grep -q 'mariabackup' "${ROOT}/Makefile"; then
    echo "Makefile must not require mariabackup after MariaDB extraction" >&2
    return 1
  fi
}

@test "unit tests use the public Ubuntu archive when the Azure mirror is set" {
  load_lib apt-mirror.sh
  dir="$(mktemp -d)"
  printf '%s\n' 'deb http://azure.archive.ubuntu.com/ubuntu noble main' > "${dir}/mirrors"
  printf '%s\n' 'deb http://archive.ubuntu.com/ubuntu noble main' > "${dir}/keep"
  flynn_ci_use_public_ubuntu_mirror "${dir}/mirrors" "${dir}/keep" "${dir}/missing"
  grep -q 'deb http://archive.ubuntu.com/ubuntu noble main' "${dir}/mirrors"
  if grep -q 'azure.archive.ubuntu.com' "${dir}/mirrors"; then
    echo "azure mirror must be replaced" >&2
    return 1
  fi
  grep -q 'deb http://archive.ubuntu.com/ubuntu noble main' "${dir}/keep"
  hosts="$(flynn_ci_ubuntu_mirrors)"
  echo "${hosts}" | grep -q 'archive.ubuntu.com'
  if ! echo "${hosts}" | grep -q 'mirrors.edge.kernel.org'; then
    echo "apt install must have a mirror other than archive.ubuntu.com" >&2
    return 1
  fi
  grep -q 'Acquire::http::Timeout' "${ROOT}/script/lib/apt-mirror.sh"
  for wf in "${ROOT}/.github/workflows/unit-tests.yml" "${ROOT}/.github/workflows/release.yml"; do
    if ! grep -q 'flynn_ci_apt_install' "${wf}"; then
      echo "${wf} must install through flynn_ci_apt_install" >&2
      return 1
    fi
  done
}

# workflow_top_permissions prints the entries of the workflow-level
# permissions block (two-space indented lines after a column-0 "permissions:").
workflow_top_permissions() {
  awk '
    /^permissions:/ { inblock = 1; sub(/^permissions:[[:space:]]*/, ""); if ($0 != "") print; next }
    inblock && /^[^[:space:]#]/ { inblock = 0 }
    inblock && /^  [^[:space:]#]/ { sub(/^[[:space:]]+/, ""); print }
  ' "$1"
}

# workflow_job prints the body of one job under "jobs:" (four-space and deeper lines).
workflow_job() {
  awk -v job="$2" '
    /^jobs:/ { injobs = 1; next }
    injobs && /^[^[:space:]#]/ { injobs = 0 }
    injobs && /^  [^[:space:]#]/ { cur = $1; sub(/:$/, "", cur); next }
    injobs && cur == job { print }
  ' "$1"
}

@test "unit workflow grants only contents: read at the top level" {
  wf="${ROOT}/.github/workflows/unit-tests.yml"
  perms="$(workflow_top_permissions "${wf}")"
  if [[ "${perms}" != "contents: read" ]]; then
    echo "top-level permissions must be exactly 'contents: read', got:" >&2
    echo "${perms}" >&2
    return 1
  fi
}

@test "unit workflow checkout does not persist the token for test code" {
  wf="${ROOT}/.github/workflows/unit-tests.yml"
  job="$(workflow_job "${wf}" unit)"
  [[ -n "${job}" ]]
  echo "${job}" | grep -q 'uses: actions/checkout@'
  if ! echo "${job}" | grep -Eq '^[[:space:]]+persist-credentials:[[:space:]]*false[[:space:]]*$'; then
    echo "unit job checkout must set persist-credentials: false" >&2
    return 1
  fi
  if echo "${job}" | grep -Eq '^    permissions:'; then
    echo "unit job must not widen permissions" >&2
    return 1
  fi
}

@test "unit workflow gives contents: write only to coverage-badge" {
  wf="${ROOT}/.github/workflows/unit-tests.yml"
  badge="$(workflow_job "${wf}" coverage-badge)"
  echo "${badge}" | grep -Eq '^      contents:[[:space:]]*write'
  jobs="$(awk '/^jobs:/{j=1;next} j && /^[^[:space:]#]/{j=0} j && /^  [^[:space:]#]/{n=$1; sub(/:$/,"",n); print n}' "${wf}")"
  for j in ${jobs}; do
    [[ "${j}" == "coverage-badge" ]] && continue
    if workflow_job "${wf}" "${j}" | grep -Eq ':[[:space:]]*write'; then
      echo "job ${j} must not get a write permission" >&2
      return 1
    fi
  done
  if workflow_top_permissions "${wf}" | grep -q 'write'; then
    echo "top-level permissions must not grant write" >&2
    return 1
  fi
}

@test "unit workflow, run-unit-tests and the unit container run go vet on discoverd" {
  validator="${ROOT}/util/commit-validator/validate-govet"
  [[ -x "${validator}" ]]
  grep -q './discoverd/...' "${validator}"
  job="$(workflow_job "${ROOT}/.github/workflows/unit-tests.yml" unit)"
  if ! echo "${job}" | grep -Eq '^[[:space:]]+run:[[:space:]]*util/commit-validator/validate-govet[[:space:]]*$'; then
    echo "unit job must run util/commit-validator/validate-govet" >&2
    return 1
  fi
  for f in "${ROOT}/script/run-unit-tests" "${ROOT}/script/docker/unit-tests/entrypoint.sh"; do
    if ! grep -q '^[[:space:]]*util/commit-validator/validate-govet' "${f}"; then
      echo "${f} must run util/commit-validator/validate-govet" >&2
      return 1
    fi
  done
}

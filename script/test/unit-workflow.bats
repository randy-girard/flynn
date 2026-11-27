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

# Packages validate-govet checks when called with no arguments (EDGE-18
# added discoverd and the edge services, NEW-23 the controller and pkg).
GOVET_PACKAGES="./discoverd/... ./router/... ./blobstore/... ./gitreceive/... ./logaggregator/... ./tarreceive/... ./cli/... ./slugbuilder/... ./controller/... ./pkg/..."

@test "unit workflow, run-unit-tests and the unit container run go vet on discoverd, the edge packages, controller and pkg" {
  validator="${ROOT}/util/commit-validator/validate-govet"
  [[ -x "${validator}" ]]
  for pkg in ${GOVET_PACKAGES}; do
    if ! grep -qF -- "${pkg}" "${validator}"; then
      echo "validate-govet must vet ${pkg} by default" >&2
      return 1
    fi
  done
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

@test "make test-scripts runs the vagrant driver contracts and every script/test-*.sh" {
  recipe="$(awk '/^test-scripts:/{r=1;next} r && /^[^\t]/{r=0} r' "${ROOT}/Makefile")"
  if ! echo "${recipe}" | grep -Eq 'test-vagrant|script/vagrant/test/\*\.sh'; then
    if ! grep -Eq '^test-scripts:.*test-vagrant' "${ROOT}/Makefile"; then
      echo "make test-scripts must run make test-vagrant (script/vagrant/test/*.sh)" >&2
      return 1
    fi
  fi
  if ! echo "${recipe}" | grep -Fq 'script/test-*.sh'; then
    echo "make test-scripts must run every script/test-*.sh" >&2
    return 1
  fi
  [[ -n "$(ls "${ROOT}"/script/test-*.sh)" ]]
}

@test "unit workflow, run-unit-tests and the unit container run make test-scripts" {
  job="$(workflow_job "${ROOT}/.github/workflows/unit-tests.yml" unit)"
  if ! echo "${job}" | grep -Eq '^[[:space:]]+run:[[:space:]]*make test-scripts[[:space:]]*$'; then
    echo "unit job must run make test-scripts" >&2
    return 1
  fi
  for f in "${ROOT}/script/run-unit-tests" "${ROOT}/script/docker/unit-tests/entrypoint.sh"; do
    if ! grep -Eq '^[[:space:]]*make test-scripts[[:space:]]*$' "${f}"; then
      echo "${f} must run make test-scripts" >&2
      return 1
    fi
  done
}

@test "script contract tests do not pipe a file reader into grep -q under pipefail" {
  # grep -q exits on the first match; under pipefail the writer's SIGPIPE
  # (exit 141) then fails the pipeline. mawk on the CI runner hits this
  # often; macOS awk rarely does. Read into a variable or a here-string.
  bad="$(grep -nE '(awk|sed|grep|cat|tar)[^|]*[|][[:space:]]*grep[^|]*[[:space:]]-[A-Za-z]*q' \
    "${ROOT}"/script/vagrant/test/*.sh "${ROOT}"/script/test-*.sh || true)"
  if [[ -n "${bad}" ]]; then
    echo "pipe into grep -q (SIGPIPE under pipefail):" >&2
    echo "${bad}" >&2
    return 1
  fi
}

@test "vagrant driver contracts skip sibling plugin checks when the checkout is absent" {
  # CI checks out only this repo (the plugin repos are private), so a check
  # that reads ../flynn-plugin-* must say it skipped instead of failing.
  for s in "${ROOT}"/script/vagrant/test/*.sh; do
    if grep -q 'ROOT}/\.\./flynn-plugin-' "${s}" && ! grep -q 'not checked out' "${s}"; then
      echo "$(basename "${s}") reads a sibling plugin checkout without skipping when it is absent" >&2
      return 1
    fi
  done
}

# workflow_jobs prints the job ids under "jobs:".
workflow_jobs() {
  awk '/^jobs:/{j=1;next} j && /^[^[:space:]#]/{j=0} j && /^  [^[:space:]#]/{n=$1; sub(/:$/,"",n); print n}' "$1"
}

# workflow_run_blocks prints every run: script (inline or block scalar).
workflow_run_blocks() {
  awk '
    function indent(s) { match(s, /^ */); return RLENGTH }
    inrun && $0 !~ /^[[:space:]]*$/ && indent($0) <= runind { inrun = 0 }
    inrun { print; next }
    /^[[:space:]]*(- )?run:/ {
      line = $0
      runind = indent(line)
      if (line ~ /^[[:space:]]*- /) runind += 2
      sub(/^[[:space:]]*(- )?run:[[:space:]]*/, "", line)
      if (line ~ /^[|>]/) { inrun = 1 } else { print line }
    }
  ' "$1"
}

@test "release workflow grants only contents: read at the top level" {
  wf="${ROOT}/.github/workflows/release.yml"
  perms="$(workflow_top_permissions "${wf}")"
  if [[ "${perms}" != "contents: read" ]]; then
    echo "top-level permissions must be exactly 'contents: read', got:" >&2
    echo "${perms}" >&2
    return 1
  fi
}

@test "release workflow gives write permissions only to publish" {
  wf="${ROOT}/.github/workflows/release.yml"
  workflow_job "${wf}" publish | grep -Eq '^      contents:[[:space:]]*write'
  workflow_job "${wf}" publish | grep -Eq '^      actions:[[:space:]]*write'
  for j in $(workflow_jobs "${wf}"); do
    [[ "${j}" == "publish" ]] && continue
    # unit-tests calls unit-tests.yml, whose coverage-badge job declares
    # contents: write. GitHub rejects the call unless the caller allows it;
    # that job only runs on push, so under workflow_dispatch it is skipped.
    if [[ "${j}" == "unit-tests" ]]; then
      perms="$(workflow_job "${wf}" "${j}" | grep -E '^      [a-z-]+:[[:space:]]*(read|write|none)')"
      if [[ "$(echo "${perms}" | tr -d ' ')" != "contents:write" ]]; then
        echo "unit-tests job must grant exactly contents: write, got: ${perms}" >&2
        return 1
      fi
      continue
    fi
    if workflow_job "${wf}" "${j}" | grep -Eq ':[[:space:]]*write'; then
      echo "job ${j} must not get a write permission" >&2
      return 1
    fi
  done
  badge_if="$(workflow_job "${ROOT}/.github/workflows/unit-tests.yml" coverage-badge | grep -E '^    if:')"
  if [[ "${badge_if}" != *"github.event_name == 'push'"* ]]; then
    echo "coverage-badge must run only on push so a release call never reaches it" >&2
    return 1
  fi
}

@test "release build jobs check out without persisting the token" {
  wf="${ROOT}/.github/workflows/release.yml"
  for j in build-base build-cluster; do
    job="$(workflow_job "${wf}" "${j}")"
    checkouts="$(echo "${job}" | grep -c 'uses: actions/checkout@' || true)"
    nopersist="$(echo "${job}" | grep -Ec '^[[:space:]]+persist-credentials:[[:space:]]*false[[:space:]]*$' || true)"
    if [[ "${checkouts}" -lt 1 || "${checkouts}" != "${nopersist}" ]]; then
      echo "job ${j}: every checkout must set persist-credentials: false (${checkouts} checkouts, ${nopersist} without credentials)" >&2
      return 1
    fi
    if echo "${job}" | grep -Eq 'secrets\.GITHUB_TOKEN|github\.token'; then
      echo "job ${j} must not reference the workflow token" >&2
      return 1
    fi
  done
}

@test "release workflow runs one release at a time" {
  wf="${ROOT}/.github/workflows/release.yml"
  block="$(awk '/^concurrency:/{c=1;next} c && /^[^[:space:]#]/{c=0} c' "${wf}")"
  echo "${block}" | grep -Eq '^  group:[[:space:]]*[^[:space:]]'
  if ! echo "${block}" | grep -Eq '^  cancel-in-progress:[[:space:]]*false'; then
    echo "release concurrency must not cancel a running release" >&2
    return 1
  fi
}

@test "release workflow runs the unit tests before any build" {
  wf="${ROOT}/.github/workflows/release.yml"
  grep -Eq '^  workflow_call:' "${ROOT}/.github/workflows/unit-tests.yml"
  first="$(workflow_jobs "${wf}" | head -n 1)"
  [[ "${first}" == "unit-tests" ]]
  workflow_job "${wf}" unit-tests | grep -Eq '^    uses:[[:space:]]*\./\.github/workflows/unit-tests\.yml[[:space:]]*$'
  if ! workflow_job "${wf}" build-base | grep -Eq '^    needs:[[:space:]]*(unit-tests|\[.*unit-tests.*\])[[:space:]]*$'; then
    echo "build-base must need unit-tests" >&2
    return 1
  fi
}

@test "release workflows pass inputs and event fields to run scripts through env" {
  for wf in "${ROOT}/.github/workflows/release.yml" "${ROOT}/.github/workflows/plugin-releases.yml"; do
    runs="$(workflow_run_blocks "${wf}")"
    [[ -n "${runs}" ]]
    if echo "${runs}" | grep -E '\$\{\{[[:space:]]*(inputs\.|github\.event\.)'; then
      echo "${wf}: run scripts must read inputs and event fields from env, not \${{ }}" >&2
      return 1
    fi
  done
}

@test "release workflows refuse a version that is not vYYYYMMDD.N" {
  for wf in "${ROOT}/.github/workflows/release.yml" "${ROOT}/.github/workflows/plugin-releases.yml"; do
    if ! workflow_run_blocks "${wf}" | grep -Fq '=~ ^v[0-9]{8}\.[0-9]+$ ]]'; then
      echo "${wf} must check the version format before using it" >&2
      return 1
    fi
  done
}

@test "workflow_run_blocks finds inline and block run scripts" {
  f="$(mktemp)"
  cat >"${f}" <<'YAML'
jobs:
  a:
    steps:
      - name: one
        run: echo ${{ inputs.x }}
      - run: |
          echo ${{ github.event.y }}
        env:
          Z: ${{ inputs.z }}
YAML
  run workflow_run_blocks "${f}"
  rm -f "${f}"
  assert_success
  assert_output $'echo ${{ inputs.x }}\n          echo ${{ github.event.y }}'
}

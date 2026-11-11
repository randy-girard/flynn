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
  for wf in "${ROOT}/.github/workflows/unit-tests.yml" "${ROOT}/.github/workflows/release.yml"; do
    if ! grep -q 'flynn_ci_use_public_ubuntu_mirror' "${wf}"; then
      echo "${wf} must rewrite the apt mirror before install" >&2
      return 1
    fi
    awk '
      /flynn_ci_use_public_ubuntu_mirror/ { seen=1 }
      /apt-get install/ {
        if (!seen) exit 1
        seen=0
      }
    ' "${wf}"
  done
}

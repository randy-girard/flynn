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

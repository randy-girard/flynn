#!/usr/bin/env bats

load "helper"

@test "make help lists public targets without building" {
  run make -C "${ROOT}" help
  assert_success
  [[ "${output}" == *"Usage:"* ]]
  for want in build test test-unit test-unit-root vagrant-setup vagrant-smoke help clean; do
    if ! printf '%s\n' "${output}" | grep -qE "^  ${want} "; then
      echo "make help must list ${want}" >&2
      echo "${output}" >&2
      return 1
    fi
  done
}

@test "Makefile documents help on the help target" {
  grep -q '^help: ##' "${ROOT}/Makefile"
  grep -qE '\.PHONY:.*\bhelp\b' "${ROOT}/Makefile"
}

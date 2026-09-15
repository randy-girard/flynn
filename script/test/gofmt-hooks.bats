#!/usr/bin/env bats

load "helper"

@test "gofmt git hooks run the same check as GitHub Actions" {
  check="${ROOT}/script/githooks/gofmt-check"
  install="${ROOT}/script/install-git-hooks"
  [[ -x "${check}" ]]
  [[ -x "${install}" ]]
  grep -q 'util/commit-validator/validate-gofmt' "${check}"
  grep -q 'pre-commit' "${install}"
  grep -q 'pre-push' "${install}"
  grep -q 'githooks/gofmt-check' "${install}"
  if grep -q 'git config' "${install}"; then
    echo "install-git-hooks must not change git config" >&2
    return 1
  fi
}

@test "install-git-hooks copies gofmt-check into a clone's hook dir" {
  tmp="$(mktemp -d "${BATS_TMPDIR}/githooks.XXXXXX")"
  git -c init.defaultBranch=main init -q "${tmp}"
  mkdir -p "${tmp}/script/githooks"
  cp "${ROOT}/script/githooks/gofmt-check" "${tmp}/script/githooks/gofmt-check"
  cp "${ROOT}/script/install-git-hooks" "${tmp}/script/install-git-hooks"
  chmod +x "${tmp}/script/githooks/gofmt-check" "${tmp}/script/install-git-hooks"
  (cd "${tmp}" && ./script/install-git-hooks)
  hooks_dir="$(git -C "${tmp}" rev-parse --git-path hooks)"
  case "${hooks_dir}" in
    /*) ;;
    *) hooks_dir="${tmp}/${hooks_dir}" ;;
  esac
  grep -q 'validate-gofmt' "${hooks_dir}/pre-commit"
  grep -q 'validate-gofmt' "${hooks_dir}/pre-push"
  [[ -x "${hooks_dir}/pre-commit" && -x "${hooks_dir}/pre-push" ]]
  rm -rf "${tmp}"
}

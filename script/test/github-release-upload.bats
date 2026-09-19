#!/usr/bin/env bats

load "helper"
load_lib "github-release-upload.sh"

setup() {
  TMP="$(mktemp -d "${BATS_TMPDIR}/gh-release.XXXXXX")"
  STUB="${TMP}/gh"
  LOG="${TMP}/gh.log"
  STATE="${TMP}/state"
  mkdir -p "${STATE}"
  cat >"${STUB}" <<'EOF'
#!/bin/bash
echo "$*" >> "${GH_LOG}"
exists="${GH_STATE}/exists"
fail_n="${GH_STATE}/upload_fail"
if [[ "$1" == "release" && "$2" == "view" ]]; then
  if [[ ! -f "${exists}" ]]; then
    exit 1
  fi
  if [[ " $* " == *" --json id "* ]]; then
    echo 42
    exit 0
  fi
  if [[ " $* " == *" --json assets "* ]]; then
    if [[ -f "${GH_STATE}/assets.tsv" ]]; then
      cat "${GH_STATE}/assets.tsv"
    fi
    exit 0
  fi
  echo "Flynn vtest"
  exit 0
fi
if [[ "$1" == "release" && "$2" == "create" ]]; then
  touch "${exists}"
  exit 0
fi
if [[ "$1" == "release" && "$2" == "upload" ]]; then
  if [[ -f "${fail_n}" ]]; then
    n=$(cat "${fail_n}")
    if [[ "${n}" -gt 0 ]]; then
      echo $((n - 1)) > "${fail_n}"
      echo "simulated upload failure" >&2
      exit 1
    fi
  fi
  exit 0
fi
if [[ "$1" == "release" && "$2" == "edit" ]]; then
  exit 0
fi
if [[ "$1" == "api" ]]; then
  exit 0
fi
exit 0
EOF
  chmod +x "${STUB}"
  export GH="${STUB}"
  export GH_LOG="${LOG}"
  export GH_STATE="${STATE}"
  export GITHUB_RELEASE_RETRY_SLEEP=0
  : >"${LOG}"
}

teardown() {
  rm -rf "${TMP}"
}

@test "github_release_list_files skips mega tarball and sorts small files first" {
  dir="${TMP}/assets"
  mkdir -p "${dir}"
  printf 'aa' > "${dir}/b.bin"
  printf 'a' > "${dir}/a.bin"
  printf 'tarball' > "${dir}/flynn-v20260917.0.tar.gz"
  run github_release_list_files "${dir}"
  assert_success
  listed="$(printf '%s\n' "${output}" | grep -v '^Skipping ')"
  [[ "${listed}" == *"a.bin"* ]]
  [[ "${listed}" == *"b.bin"* ]]
  if printf '%s\n' "${listed}" | grep -q 'flynn-v20260917.0.tar.gz'; then
    echo "mega tarball must not be uploaded" >&2
    return 1
  fi
  first="$(printf '%s\n' "${listed}" | head -n1)"
  [[ "${first}" == */a.bin ]]
}

@test "github_release_list_files rejects 2GiB assets" {
  dir="${TMP}/assets"
  mkdir -p "${dir}"
  truncate -s 2147483648 "${dir}/too-big.squashfs"
  run github_release_list_files "${dir}"
  assert_failure
}

@test "github_release_publish creates a draft then uploads one file at a time" {
  dir="${TMP}/assets"
  mkdir -p "${dir}"
  echo notes > "${TMP}/notes.md"
  printf 'x' > "${dir}/install-flynn-cli"
  printf 'yy' > "${dir}/layer.squashfs"
  run github_release_publish \
    --version v20990101.0 \
    --repo acme/flynn \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --dir "${dir}" \
    --draft false \
    --prerelease false
  assert_success
  grep -q 'release create v20990101.0' "${LOG}"
  grep 'release create' "${LOG}" | grep -q -- '--draft'
  grep -q 'release upload v20990101.0' "${LOG}"
  grep -q -- '--clobber' "${LOG}"
  grep 'release edit' "${LOG}" | grep -q -- '--draft=false'
  if grep 'release create' "${LOG}" | grep -q 'install-flynn-cli'; then
    echo "create must not take asset files" >&2
    cat "${LOG}" >&2
    return 1
  fi
  uploads="$(grep -c 'release upload v20990101.0' "${LOG}")"
  [[ "${uploads}" -eq 2 ]]
}

@test "github_release_publish reuses an existing release and skips matching assets" {
  touch "${STATE}/exists"
  printf 'install-flynn-cli\t2\n' > "${STATE}/assets.tsv"
  dir="${TMP}/assets"
  mkdir -p "${dir}"
  echo notes > "${TMP}/notes.md"
  printf 'ab' > "${dir}/install-flynn-cli"
  printf 'z' > "${dir}/checksums.sha512"
  run github_release_publish \
    --version v20990101.0 \
    --repo acme/flynn \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --dir "${dir}" \
    --draft true \
    --prerelease false
  assert_success
  if grep -q 'release create' "${LOG}"; then
    echo "existing release must be reused" >&2
    cat "${LOG}" >&2
    return 1
  fi
  if grep 'release upload' "${LOG}" | grep -q 'install-flynn-cli'; then
    echo "already-uploaded asset must be skipped" >&2
    cat "${LOG}" >&2
    return 1
  fi
  grep -q 'checksums.sha512' "${LOG}"
  if grep -q -- '--draft=false' "${LOG}"; then
    echo "requested drafts must stay drafts" >&2
    cat "${LOG}" >&2
    return 1
  fi
}

@test "github_release_publish retries a failed upload" {
  echo 1 > "${STATE}/upload_fail"
  dir="${TMP}/assets"
  mkdir -p "${dir}"
  echo notes > "${TMP}/notes.md"
  printf 'x' > "${dir}/only.bin"
  export GITHUB_RELEASE_UPLOAD_ATTEMPTS=2
  run github_release_publish \
    --version v20990101.0 \
    --repo acme/flynn \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --dir "${dir}" \
    --draft true \
    --prerelease false
  assert_success
  uploads="$(grep -c 'release upload v20990101.0' "${LOG}")"
  [[ "${uploads}" -eq 2 ]]
}

@test "release workflow uploads through github_release_publish" {
  wf="${ROOT}/.github/workflows/release.yml"
  grep -q 'github-release-upload.sh' "${wf}"
  grep -q 'github_release_publish' "${wf}"
  if ! grep -A6 'name: Publish GitHub release' "${wf}" | grep -q 'timeout-minutes: 60'; then
    echo "publish job must keep a timeout while assets upload one-by-one" >&2
    return 1
  fi
  if grep 'gh release create' "${wf}" | grep -q 'FILES'; then
    echo "workflow must not pass every asset to gh release create" >&2
    return 1
  fi
  grep -q 'github_release_publish' "${ROOT}/script/release"
}

@test "github_release_publish fails when images.json lists a missing squashfs" {
  dir="${TMP}/assets"
  mkdir -p "${dir}"
  echo notes > "${TMP}/notes.md"
  printf 'x' > "${dir}/install-flynn-cli"
  cat >"${dir}/images.json" <<'EOF'
{"slugrunner":{"manifest":{"rootfs":[{"layers":[{"id":"deadbeef"}]}]}}}
EOF
  run github_release_publish \
    --version v20990101.0 \
    --repo acme/flynn \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --dir "${dir}" \
    --draft true \
    --prerelease false
  assert_failure
  [[ "${output}" == *deadbeef* ]]
}

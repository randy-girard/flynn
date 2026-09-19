#!/usr/bin/env bats

load "helper"
load_lib "release-layers.sh"

setup() {
  TMP="$(mktemp -d "${BATS_TMPDIR}/release-layers.XXXXXX")"
}

teardown() {
  rm -rf "${TMP}"
}

write_images_json() {
  cat >"$1" <<'EOF'
{
  "slugrunner": {
    "manifest": {
      "rootfs": [
        {"layers": [{"id": "aaa111", "length": 4096}, {"id": "bbb222", "length": 8}]}
      ]
    }
  },
  "busybox": {
    "manifest": {
      "rootfs": [
        {"layers": [{"id": "aaa111", "length": 4096}]}
      ]
    }
  }
}
EOF
}

@test "flynn_images_json_layer_ids lists unique ids" {
  write_images_json "${TMP}/images.json"
  run flynn_images_json_layer_ids "${TMP}/images.json"
  assert_success
  [[ "${output}" == *aaa111* ]]
  [[ "${output}" == *bbb222* ]]
  count="$(printf '%s\n' "${output}" | grep -c .)"
  [[ "${count}" -eq 2 ]]
}

@test "flynn_verify_images_json_layers fails when a squashfs is missing" {
  write_images_json "${TMP}/images.json"
  : > "${TMP}/aaa111.squashfs"
  run flynn_verify_images_json_layers "${TMP}/images.json" "${TMP}"
  assert_failure
  [[ "${output}" == *bbb222* ]]
}

@test "flynn_verify_images_json_layers succeeds when every layer blob exists" {
  write_images_json "${TMP}/images.json"
  : > "${TMP}/aaa111.squashfs"
  : > "${TMP}/bbb222.squashfs"
  run flynn_verify_images_json_layers "${TMP}/images.json" "${TMP}"
  assert_success
}

@test "flynn_images_json_layer_ids reads gzipped images.json" {
  write_images_json "${TMP}/images.json"
  gzip -c "${TMP}/images.json" > "${TMP}/images.json.gz"
  run flynn_images_json_layer_ids "${TMP}/images.json.gz"
  assert_success
  [[ "${output}" == *aaa111* ]]
}

@test "release packaging verifies images.json layers" {
  rel="${ROOT}/script/release"
  grep -q 'flynn_verify_images_json_layers' "${rel}"
  grep -q 'package_layers copies only images.json layer blobs' "${rel}"
  grep -q 'github_release_verify_images_json_layers' "${ROOT}/script/lib/github-release-upload.sh"
  grep -q 'flynn_verify_images_json_layers' "${ROOT}/.github/workflows/release.yml"
}

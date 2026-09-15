#!/usr/bin/env bats

load "helper"

@test "production image builds omit cluster-test images" {
  builder="${ROOT}/builder/build.go"
  build_sh="${ROOT}/build.sh"
  wf="${ROOT}/.github/workflows/release.yml"
  images_tmpl="${ROOT}/util/release/images_template.json"

  grep -q '"test-apps"' "${builder}"
  grep -q '"controller-examples"' "${builder}"
  grep -q 'isTestImage' "${builder}"

  grep -q 'run_flynn_builder_only apps' "${build_sh}"
  grep -q 'run_flynn_builder_only test' "${build_sh}"
  if grep -A20 'run_phase_cluster()' "${build_sh}" | grep -q 'run_phase_test'; then
    echo "build.sh cluster must not build test images" >&2
    return 1
  fi

  grep -q 'build.sh --version "${{ steps.version.outputs.VERSION }}" apps' "${wf}"
  if grep 'sudo -E ./build.sh' "${wf}" | grep -Eq '(^|[[:space:]])test([[:space:]]|$)'; then
    echo "release workflow must not run build.sh test" >&2
    grep 'sudo -E ./build.sh' "${wf}" >&2
    return 1
  fi

  if grep -E '\$image_artifact\[(test|test-apps|controller-examples)\]' "${images_tmpl}"; then
    echo "release images template must not reference cluster-test images" >&2
    return 1
  fi
}

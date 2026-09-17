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

@test "production host builds omit flynn-test binaries" {
  build_flynn="${ROOT}/script/build-flynn"
  build_sh="${ROOT}/build.sh"
  unit_wf="${ROOT}/.github/workflows/unit-tests.yml"
  release_wf="${ROOT}/.github/workflows/release.yml"
  integ="${ROOT}/script/run-integration-tests"

  grep -q -- '--test-binaries' "${build_flynn}"
  grep -q 'FLYNN_BUILD_TEST_BINARIES' "${build_flynn}"
  grep -q 'skipping flynn-test binaries' "${build_flynn}"

  # Default production path must not pass --test-binaries.
  if grep 'script/build-flynn' "${build_sh}" | grep -q -- '--test-binaries'; then
    echo "build.sh must not compile flynn-test host binaries" >&2
    return 1
  fi
  if grep 'script/build-flynn' "${unit_wf}" "${release_wf}" | grep -q -- '--test-binaries'; then
    echo "CI must not compile flynn-test host binaries" >&2
    return 1
  fi
  if grep 'FLYNN_BUILD_TEST_BINARIES' "${unit_wf}" "${release_wf}" "${build_sh}"; then
    echo "CI/build.sh must not set FLYNN_BUILD_TEST_BINARIES" >&2
    return 1
  fi

  grep -q 'FLYNN_BUILD_TEST_BINARIES=1 make' "${integ}"
}

@test "GitHub image builds cap concurrency and hide retried Go compiler annotations" {
  wf="${ROOT}/.github/workflows/release.yml"
  build_sh="${ROOT}/build.sh"
  builder="${ROOT}/builder/build.go"

  grep -q "app image builds (phase 2; default 2" "${wf}"
  grep -q '::remove-matcher owner=go::' "${wf}"
  grep -q 'FLYNN_GO_BUILD_P' "${wf}"
  grep -q 'FLYNN_IMAGE_BUILD_TIMEOUT' "${wf}"
  grep -q 'timeout-minutes: 90' "${wf}"
  grep -q 'sleep 60' "${wf}"
  grep -q 'flynn-host ps' "${wf}"

  grep -q 'goBuildParallelFlag' "${builder}"
  grep -q 'loadImageDirArtifacts' "${builder}"
  grep -q 'persisted successful image artifacts for retry' "${builder}"
  grep -q 'image builds still running' "${builder}"
  grep -q 'buildImageWithTimeout' "${builder}"

  grep -q 'default_gomemlimit' "${build_sh}"
  grep -q 'default_builder_max_retries' "${build_sh}"
  grep -q 'Reducing concurrency' "${build_sh}"
}

@test "CLI builds omit 32-bit x86" {
  manifest="${ROOT}/builder/manifest.json.template"
  manifest_json="${ROOT}/builder/manifest.json"
  build_flynn="${ROOT}/script/build-flynn"
  release="${ROOT}/script/release"
  notes="${ROOT}/script/lib/release-notes.sh"
  pkg="${ROOT}/script/package-github-release"
  install_cli="${ROOT}/script/install-flynn-cli"
  install_release="${ROOT}/script/install-flynn-release"
  smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

  grep -q '"id": "cli-linux-amd64"' "${manifest}"
  grep -q '"id": "cli-linux-arm64"' "${manifest}"
  grep -q '"id": "cli-windows-amd64"' "${manifest}"
  if grep -E 'cli-(linux|windows)-386|"GOARCH": "386"|linux/386|windows/386|flynn-linux-386' \
      "${manifest}" "${manifest_json}" "${build_flynn}" "${release}" "${notes}" "${pkg}"; then
    echo "386 CLI images/binaries must not be built or packaged" >&2
    return 1
  fi

  grep -q '32-bit x86 is not supported' "${install_cli}"
  grep -q '32-bit x86 is not supported' "${install_release}"
  if grep -E 'echo "386"|arch="386"|cli_arch=386' "${install_cli}" "${install_release}" "${smoke}"; then
    echo "installers/smoke must not map i386/i686 to 386" >&2
    return 1
  fi
}

#!/usr/bin/env bats

load "helper"

@test "bootstrap-flynn passes a non-interactive admin to flynn-host bootstrap" {
  grep -q -- '--admin-email' "${ROOT}/script/bootstrap-flynn"
  grep -q -- '--admin-password' "${ROOT}/script/bootstrap-flynn"
}

@test "integration runner finds the cluster add line and logs the test job in" {
  grep -qE 'flynn cluster\(:add\| add\)' "${ROOT}/script/run-integration-tests"
  grep -q 'FLYNN_EMAIL=' "${ROOT}/script/run-integration-tests"
  grep -q 'FLYNN_PASSWORD=' "${ROOT}/script/run-integration-tests"
}

@test "image builds load host.json secrets before flynn-builder" {
  awk '/^run_flynn_builder_only\(\)/,/^run_phase_toolchain\(\)/' "${ROOT}/build.sh" | grep -q export_host_json_secrets
}

@test "integration runner builds cluster-test images when they are missing" {
  grep -q 'build/image/test.json' "${ROOT}/script/run-integration-tests"
  grep -q 'build.sh" test' "${ROOT}/script/run-integration-tests"
}

@test "integration runner rebuilds test-apps when test app sources change" {
  grep -q 'test/apps' "${ROOT}/script/run-integration-tests"
  grep -q 'test-apps.json' "${ROOT}/script/run-integration-tests"
}

@test "integration runner bind-mounts the host docker client" {
  grep -q '/usr/bin/docker:/usr/local/bin/docker' "${ROOT}/script/run-integration-tests"
  grep -q 'cli-plugins' "${ROOT}/script/run-integration-tests"
}

@test "build-flynn refreshes the CLI when cli sources change" {
  grep -q 'cli sources newer than build artifact' "${ROOT}/script/build-flynn"
}

@test "integration runner finds the Vagrant Go toolchain" {
  grep -q '/usr/local/go/bin' "${ROOT}/script/run-integration-tests"
}

@test "integration runner registers qemu-user binfmt on aarch64" {
  grep -q 'ensure-qemu-binfmt.sh' "${ROOT}/script/run-integration-tests"
}

@test "integration runner passes the controller job key into the test process" {
  grep -q 'ENV\[AUTH_KEY\]' "${ROOT}/script/run-integration-tests"
  grep -q 'CONTROLLER_KEY=' "${ROOT}/script/run-integration-tests"
}

@test "nested test clusters pass a non-interactive admin" {
  grep -q -- '--admin-email' "${ROOT}/test/cluster2/cluster.go"
  grep -q -- '--admin-password' "${ROOT}/test/cluster2/cluster.go"
}

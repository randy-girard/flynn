#!/usr/bin/env bats

load "helper"

setup() {
  unset FLYNN_ALLOWED_BUILDPACKS
  source "${ROOT}/slugbuilder/builder/buildpack-policy.sh"
}

@test "no allowlist allows every buildpack" {
  run buildpack_allowed ruby
  assert_success
  FLYNN_ALLOWED_BUILDPACKS=" , "
  run buildpack_allowed ruby
  assert_success
}

@test "bundled directories map to policy names" {
  [[ "$(buildpack_policy_name /builder/buildpacks/000004_heroku-buildpack-nodejs)" == "nodejs" ]]
  [[ "$(buildpack_policy_name /builder/buildpacks/000001_heroku-buildpack-multi)" == "multi" ]]
  [[ "$(buildpack_policy_name /builder/buildpacks/000002_staticfile-buildpack)" == "static" ]]
  [[ "$(buildpack_policy_name /builder/buildpacks/000011_heroku-buildpack-go)" == "go" ]]
}

@test "allowlist accepts comma or space separated names, case-insensitive" {
  FLYNN_ALLOWED_BUILDPACKS="Go, nodejs python"
  for name in go nodejs python NodeJS; do
    run buildpack_allowed "${name}"
    assert_success
  done
  run buildpack_allowed ruby
  assert_failure
}

@test "filter keeps allowed bundled buildpacks in order" {
  FLYNN_ALLOWED_BUILDPACKS="go,ruby"
  run buildpack_policy_filter \
    /b/000003_heroku-buildpack-ruby \
    /b/000004_heroku-buildpack-nodejs \
    /b/000011_heroku-buildpack-go
  assert_success
  [[ "${output}" == $'/b/000003_heroku-buildpack-ruby\n/b/000011_heroku-buildpack-go' ]]
}

@test "custom URLs match exactly or without the ref" {
  FLYNN_ALLOWED_BUILDPACKS="https://github.com/acme/buildpack.git"
  run buildpack_url_allowed "https://github.com/acme/buildpack.git#v2"
  assert_success
  run buildpack_url_allowed "https://github.com/evil/heroku-buildpack-go.git"
  assert_failure
  FLYNN_ALLOWED_BUILDPACKS="go"
  run buildpack_url_allowed "https://github.com/heroku/heroku-buildpack-go.git"
  assert_failure
}

@test "inline buildpacks need the inline entry" {
  FLYNN_ALLOWED_BUILDPACKS="go"
  run buildpack_allowed inline
  assert_failure
  FLYNN_ALLOWED_BUILDPACKS="go inline"
  run buildpack_allowed inline
  assert_success
}

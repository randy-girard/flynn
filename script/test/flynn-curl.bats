#!/usr/bin/env bats

load "helper"

SHIM="${BATS_TEST_DIRNAME}/../../builder/img/flynn-curl.sh"

setup() {
  TMP="$(mktemp --directory)"
  export FLYNN_HTTP_CACHE_ROOT="${TMP}/cache"
  mkdir -p "${FLYNN_HTTP_CACHE_ROOT}"

  # Stub real curl: logs each invocation and writes a body that
  # is unique per-call so cache-hits vs. cache-misses are observable.
  export LOG="${TMP}/log"
  : >"${LOG}"
  cat >"${TMP}/curl" <<'EOF'
#!/bin/bash
echo "call" >> "${LOG}"
out=""
prev=""
for a in "$@"; do
  if [[ "${prev}" == "-o" || "${prev}" == "--output" ]]; then out="${a}"; fi
  prev="${a}"
done
body="body-${RANDOM}-${BASHPID}-$(date +%s%N)"
if [[ -n "${out}" ]]; then
  echo "${body}" > "${out}"
else
  echo "${body}"
fi
exit 0
EOF
  chmod +x "${TMP}/curl"
  export REAL_CURL="${TMP}/curl"
}

teardown() {
  rm -rf "${TMP}"
  unset FLYNN_HTTP_CACHE_ROOT REAL_CURL LOG
  unset FLYNN_NO_HTTP_CACHE FLYNN_HTTP_CACHE_SKIP_PATTERNS
}

# call_count returns the number of times the stub real-curl was invoked.
call_count() {
  wc -l <"${LOG}" | tr -d ' '
}

@test "static URL is cached: second call hits cache" {
  bash "${SHIM}" -o "${TMP}/a1" "https://example.com/static/file.tar.gz"
  bash "${SHIM}" -o "${TMP}/a2" "https://example.com/static/file.tar.gz"
  [[ "$(call_count)" == "1" ]]
  diff -q "${TMP}/a1" "${TMP}/a2"
}

@test "/current/ URLs bypass the cache" {
  bash "${SHIM}" -o "${TMP}/b1" \
    "https://cloud-images.ubuntu.com/releases/noble/current/some.img"
  bash "${SHIM}" -o "${TMP}/b2" \
    "https://cloud-images.ubuntu.com/releases/noble/current/some.img"
  [[ "$(call_count)" == "2" ]]
  run diff -q "${TMP}/b1" "${TMP}/b2"
  assert_failure
}

@test "SHA256SUMS URL bypasses the cache" {
  local url="https://cloud-images.ubuntu.com/releases/noble/release/SHA256SUMS"
  bash "${SHIM}" -o "${TMP}/c1" "${url}"
  bash "${SHIM}" -o "${TMP}/c2" "${url}"
  [[ "$(call_count)" == "2" ]]
  run diff -q "${TMP}/c1" "${TMP}/c2"
  assert_failure
}

@test "SHA256SUMS.gpg URL bypasses the cache" {
  local url="https://cloud-images.ubuntu.com/releases/noble/release/SHA256SUMS.gpg"
  bash "${SHIM}" -o "${TMP}/d1" "${url}"
  bash "${SHIM}" -o "${TMP}/d2" "${url}"
  [[ "$(call_count)" == "2" ]]
}

@test "FLYNN_HTTP_CACHE_SKIP_PATTERNS bypasses matching URLs" {
  export FLYNN_HTTP_CACHE_SKIP_PATTERNS="weather:rolling"
  bash "${SHIM}" -o "${TMP}/e1" "https://api.example.com/v1/weather/today.json"
  bash "${SHIM}" -o "${TMP}/e2" "https://api.example.com/v1/weather/today.json"
  [[ "$(call_count)" == "2" ]]
}

@test "FLYNN_HTTP_CACHE_SKIP_PATTERNS leaves non-matching URLs cached" {
  export FLYNN_HTTP_CACHE_SKIP_PATTERNS="weather:rolling"
  bash "${SHIM}" -o "${TMP}/f1" "https://api.example.com/v1/static/today.json"
  bash "${SHIM}" -o "${TMP}/f2" "https://api.example.com/v1/static/today.json"
  [[ "$(call_count)" == "1" ]]
}

@test "FLYNN_NO_HTTP_CACHE=1 bypasses cache entirely" {
  export FLYNN_NO_HTTP_CACHE=1
  bash "${SHIM}" -o "${TMP}/g1" "https://example.com/static/file.tar.gz"
  bash "${SHIM}" -o "${TMP}/g2" "https://example.com/static/file.tar.gz"
  [[ "$(call_count)" == "2" ]]
}

@test "non-GET methods delegate to real curl uncached" {
  bash "${SHIM}" -X POST -o "${TMP}/h1" "https://example.com/api/submit"
  bash "${SHIM}" --request=PUT -o "${TMP}/h2" "https://example.com/api/submit"
  [[ "$(call_count)" == "2" ]]
}

@test "without FLYNN_HTTP_CACHE_ROOT the shim is a passthrough" {
  unset FLYNN_HTTP_CACHE_ROOT
  bash "${SHIM}" -o "${TMP}/i1" "https://example.com/static/file.tar.gz"
  bash "${SHIM}" -o "${TMP}/i2" "https://example.com/static/file.tar.gz"
  [[ "$(call_count)" == "2" ]]
}

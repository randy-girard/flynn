#!/bin/bash

set -e

export HOME="/root"
export PATH="${ROOT}/build/bin:${PATH}"
# flynn-test is compiled on the host. gocheck opens those paths when an
# assertion fails, and the job bind-mounts the tree at ${ROOT}.
if [[ ! -e /root/go/src/github.com/flynn/flynn ]]; then
  mkdir -p /root/go/src/github.com/flynn
  ln -s "${ROOT}" /root/go/src/github.com/flynn/flynn
fi
export BACKOFF_PERIOD="5s"
# docker export and slug upload shell out to curl. The baked test image
# predates that package; install it when the image does not have it.
if ! command -v curl >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq curl ca-certificates
fi

main() {
  if [[ -n "${ROUTER_IP}" ]] && [[ -n "${DOMAIN}" ]]; then
    echo "${ROUTER_IP}" \
      "${DOMAIN}" \
      "controller.${DOMAIN}" \
      "auth.${DOMAIN}" \
      "git.${DOMAIN}" \
      "images.${DOMAIN}" \
      >> /etc/hosts
  fi

  flynn cluster add ${CLUSTER_ADD_ARGS}

  cd "${ROOT}/test"

  # Binary is built on the host (script/build-flynn); the job image does not ship a working Go toolchain.
  ft="${ROOT}/build/bin/flynn-test"
  if [[ ! -x "${ft}" ]]; then
    echo >&2 "error: missing ${ft}; run script/build-flynn --test-binaries (or FLYNN_BUILD_TEST_BINARIES=1 make) on the host."
    exit 127
  fi
  exec "${ft}" "$@"
}

main "$@"

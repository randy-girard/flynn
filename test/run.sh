#!/bin/bash

set -e

export HOME="/root"
export PATH="${ROOT}/build/bin:${PATH}"
export BACKOFF_PERIOD="5s"

main() {
  if [[ -n "${ROUTER_IP}" ]] && [[ -n "${DOMAIN}" ]]; then
    echo "${ROUTER_IP}" \
      "${DOMAIN}" \
      "controller.${DOMAIN}" \
      "git.${DOMAIN}" \
      "images.${DOMAIN}" \
      >> /etc/hosts
  fi

  flynn cluster add ${CLUSTER_ADD_ARGS}

  cd "${ROOT}/test"

  # Binary is built on the host (script/build-flynn); the job image does not ship a working Go toolchain.
  ft="${ROOT}/build/bin/flynn-test"
  if [[ ! -x "${ft}" ]]; then
    echo >&2 "error: missing ${ft}; run make build (or script/build-flynn) on the host."
    exit 127
  fi
  exec "${ft}" "$@"
}

main "$@"

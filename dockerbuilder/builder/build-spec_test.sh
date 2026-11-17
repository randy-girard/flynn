#!/bin/bash
set -eo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./build.sh
spec=$'1\nart-worker\nworker\nDockerfile\nproduction\n1\nFOO\nbar\n'
read_build_spec "${spec}"
[[ "${SPEC_COUNT}" == "1" ]]
[[ "${SPEC_ID[0]}" == "art-worker" ]]
[[ "${SPEC_CTX[0]}" == "worker" ]]
[[ "${SPEC_FILE[0]}" == "Dockerfile" ]]
[[ "${SPEC_TARGET[0]}" == "production" ]]
[[ "${SPEC_ARG_KEYS[0]}" == "FOO" ]]
[[ "${SPEC_ARG_VALS[0]}" == "bar" ]]
if grep -n -E 'echo .*FLYNN_BUILD_SPEC|echo .*SPEC_ARG' build.sh; then
  echo "build script prints the spec or a build-arg" >&2
  exit 1
fi
echo ok

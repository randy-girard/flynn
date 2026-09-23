#!/bin/bash
# Regression: smoke configurations come from a matrix document (example +
# gitignored local), not only environment variables. Env still overrides
# individual fields. --item runs one named configuration.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
entry="${ROOT}/script/vagrant-smoke.sh"
example="${ROOT}/smoke-matrix.example.yaml"
py="${ROOT}/script/lib/smoke-matrix.py"
docs="${ROOT}/docs/content/development.html.md"
gitignore="${ROOT}/.gitignore"

# vagrant-upgrade-smoke.sh snapshots matrix-related env (SKIP_BUILD, KEEP_*,
# …) into SMOKE_MATRIX_EXPLICIT before it runs this script. That snapshot is
# for the live smoke run. It must not hide file-driven exports this
# regression asserts (defaults.skip_build → SKIP_BUILD=1, and the same for
# other SKIP_* keys). An empty snapshot still lets the explicit-win check
# below override one key.
export SMOKE_MATRIX_EXPLICIT='{}'
unset SKIP_BUILD

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -Fq -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing ${needle} in ${file}" >&2
    exit 1
  fi
}

need "${smoke}" 'parse_smoke_cli' \
  "smoke must parse --item / --list / --matrix before applying defaults"
need "${smoke}" 'smoke-matrix.example.yaml' \
  "smoke header must document the committed example matrix"
need "${smoke}" 'smoke-matrix.yaml' \
  "smoke header must document the gitignored local matrix"
need "${smoke}" '--item' \
  "smoke must accept --item to run one matrix configuration"
need "${smoke}" '--item quick' \
  "smoke header must document the contributor quick item"
need "${smoke}" '--item minio' \
  "smoke header must document the MinIO blobstore item"
need "${smoke}" 'SMOKE_BLOBSTORE_BACKEND' \
  "smoke must honor SMOKE_BLOBSTORE_BACKEND / blobstore_backend"
need "${smoke}" 'start_minio_sidecar' \
  "smoke must run MinIO outside Flynn so --clean restore keeps objects"
need "${smoke}" 'flynn-host blobstore:set' \
  "minio smoke must configure blobstore through flynn-host blobstore:set"
need "${smoke}" 'SKIP_BUILDPACK' \
  "smoke must honor SKIP_BUILDPACK / skip_buildpack"
need "${smoke}" 'SMOKE_DATASTORES' \
  "smoke must honor SMOKE_DATASTORES / datastores"
need "${smoke}" 'WAIT_FOR_INTERVAL' \
  "smoke must poll wait_for faster than a fixed 5s sleep"
need "${smoke}" 'datastore_wanted' \
  "smoke must skip unselected datastore engines"
need "${smoke}" 'SMOKE_MATRIX_ITEM' \
  "SMOKE_MATRIX_ITEM must be an env alias for --item"
need "${smoke}" 'run_smoke_topologies' \
  "each matrix item must reuse the topology loop"
need "${smoke}" 'FLYNN_MAX_NODES_FLOOR' \
  "inventory must cover every selected item before the first vagrant up"
need "${entry}" '--item singleton' \
  "vagrant-smoke.sh must document --item"
need "${example}" 'id: singleton' \
  "example matrix must include the 1-node configuration"
need "${example}" 'id: ha' \
  "example matrix must include the 3-node HA configuration"
need "${example}" 'id: add-node' \
  "example matrix must include add-node"
need "${example}" 'id: remove-node' \
  "example matrix must include remove-node"
need "${example}" 'id: discovery' \
  "example matrix must include discovery"
need "${example}" 'id: install-only' \
  "example matrix must include a skip-upgrade/skip-backup iterate profile"
need "${example}" 'id: quick' \
  "example matrix must include the contributor quick smoke"
need "${example}" 'id: minio' \
  "example matrix must include the MinIO S3-compatible blobstore profile"
need "${example}" 'blobstore_backend: minio' \
  "minio profile must set blobstore_backend"
need "${example}" 'enabled: false' \
  "membership/discovery/iterate/quick/minio profiles must be opt-in in the example"
need "${docs}" '--item quick' \
  "development docs must tell contributors to run --item quick"
need "${gitignore}" '/smoke-matrix.yaml' \
  ".gitignore must ignore the local smoke-matrix.yaml"
need "${docs}" 'smoke-matrix.example.yaml' \
  "development docs must describe the smoke matrix files"
need "${docs}" '--item' \
  "development docs must show running a single matrix item"
need "${py}" 'snapshot-env' \
  "matrix loader must snapshot explicit env so file values do not clobber overrides"

if grep -qE '^/smoke-matrix.example.yaml' "${gitignore}"; then
  echo "example matrix must not be gitignored" >&2
  exit 1
fi

listed="$(bash "${entry}" --list)"
echo "${listed}" | grep -q 'matrix:' \
  || { echo "--list must print the matrix path" >&2; echo "${listed}" >&2; exit 1; }
echo "${listed}" | grep -q 'singleton' \
  || { echo "--list must include singleton" >&2; echo "${listed}" >&2; exit 1; }
echo "${listed}" | grep -q 'quick' \
  || { echo "--list must include quick" >&2; echo "${listed}" >&2; exit 1; }
echo "${listed}" | grep -q 'minio' \
  || { echo "--list must include minio" >&2; echo "${listed}" >&2; exit 1; }

out="$(python3 "${py}" --root "${ROOT}" --matrix "${example}" select)"
if [[ "${out}" != $'singleton\nha' ]]; then
  echo "enabled example items must be singleton then ha, got '${out}'" >&2
  exit 1
fi

out="$(python3 "${py}" --root "${ROOT}" --matrix "${example}" --item install-only apply-item)"
echo "${out}" | grep -q 'SMOKE_TOPOLOGIES=1' \
  || { echo "install-only must set topologies=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_UPGRADE=1' \
  || { echo "install-only must set SKIP_UPGRADE=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_BACKUP=1' \
  || { echo "install-only must set SKIP_BACKUP=1" >&2; echo "${out}" >&2; exit 1; }

out="$(python3 "${py}" --root "${ROOT}" --matrix "${example}" --item quick apply-item)"
echo "${out}" | grep -q 'SMOKE_TOPOLOGIES=1' \
  || { echo "quick must set topologies=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_UPGRADE=1' \
  || { echo "quick must set SKIP_UPGRADE=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_BACKUP=1' \
  || { echo "quick must set SKIP_BACKUP=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_PLUGIN_INSTALL=1' \
  || { echo "quick must set SKIP_PLUGIN_INSTALL=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_BUILDPACK=1' \
  || { echo "quick must set SKIP_BUILDPACK=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SMOKE_DATASTORES=postgres' \
  || { echo "quick must set SMOKE_DATASTORES=postgres" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q "PLUGIN_SMOKE_APPS=''" \
  || { echo "quick must clear PLUGIN_SMOKE_APPS" >&2; echo "${out}" >&2; exit 1; }

out="$(python3 "${py}" --root "${ROOT}" --matrix "${example}" --item minio apply-item)"
echo "${out}" | grep -q 'SMOKE_TOPOLOGIES=1' \
  || { echo "minio must set topologies=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_UPGRADE=1' \
  || { echo "minio must set SKIP_UPGRADE=1" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SMOKE_BLOBSTORE_BACKEND=minio' \
  || { echo "minio must set SMOKE_BLOBSTORE_BACKEND=minio" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q "SMOKE_DATASTORES='postgres mysql'" \
  || echo "${out}" | grep -q 'SMOKE_DATASTORES=postgres mysql' \
  || { echo "minio must set SMOKE_DATASTORES=postgres mysql" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q "PLUGIN_SMOKE_APPS='mysql'" \
  || echo "${out}" | grep -q 'PLUGIN_SMOKE_APPS=mysql' \
  || { echo "minio must install the mysql plugin" >&2; echo "${out}" >&2; exit 1; }
if echo "${out}" | grep -q 'SKIP_BACKUP=1'; then
  echo "minio must run backup/restore" >&2
  echo "${out}" >&2
  exit 1
fi

out="$(python3 "${py}" --root "${ROOT}" --matrix "${example}" --item quick apply-run)"
echo "${out}" | grep -q 'SKIP_PLUGIN_INSTALL=1' \
  || { echo "quick apply-run must skip plugin image builds" >&2; echo "${out}" >&2; exit 1; }

out="$(SMOKE_MATRIX_EXPLICIT='{"SKIP_UPGRADE":"0"}' python3 "${py}" --root "${ROOT}" --matrix "${example}" --item install-only apply-item)"
if echo "${out}" | grep -q 'SKIP_UPGRADE='; then
  echo "explicit SKIP_UPGRADE must win over the matrix file, got:" >&2
  echo "${out}" >&2
  exit 1
fi

if python3 "${py}" --root "${ROOT}" --matrix "${example}" --item nope select >/dev/null 2>&1; then
  echo "unknown --item must fail" >&2
  exit 1
fi

if bash "${entry}" --item nope >/dev/null 2>&1; then
  echo "smoke --item nope must fail before Vagrant" >&2
  exit 1
fi

tmp="$(mktemp)"
cat > "${tmp}" <<'EOF'
version: 1
defaults:
  skip_build: true
items:
  - id: only
    topologies: "3"
    skip_cli: true
EOF
out="$(python3 "${py}" --root "${ROOT}" --matrix "${tmp}" apply-run)"
echo "${out}" | grep -q 'SKIP_BUILD=1' \
  || { echo "defaults.skip_build must export SKIP_BUILD=1" >&2; echo "${out}" >&2; exit 1; }
out="$(python3 "${py}" --root "${ROOT}" --matrix "${tmp}" --item only apply-item)"
echo "${out}" | grep -q 'SMOKE_TOPOLOGIES=3' \
  || { echo "item topologies must export SMOKE_TOPOLOGIES=3" >&2; echo "${out}" >&2; exit 1; }
echo "${out}" | grep -q 'SKIP_CLI=1' \
  || { echo "item skip_cli must export SKIP_CLI=1" >&2; echo "${out}" >&2; exit 1; }
inv="$(python3 "${py}" --root "${ROOT}" --matrix "${tmp}" max-inventory)"
if [[ "${inv}" != "3" ]]; then
  echo "max-inventory for topologies 3 must be 3, got ${inv}" >&2
  exit 1
fi
rm -f "${tmp}"

echo "ok smoke matrix is file-driven with env overrides and --item"

#!/bin/bash
# Regression: cluster image slimming must not silently grow back, and runtime
# smoke must still prove appliances work after busybox/zstd/apt/strip changes.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
manifest="${ROOT}/builder/manifest.json.template"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
wrapper="${ROOT}/builder/go-wrapper.sh"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

# Print the JSON object for image id from the builder manifest template.
image_block() {
  local id=$1
  awk -v id="${id}" '
    $0 ~ "\"id\": \"" id "\"" {p=1}
    p {print}
    p && /"id": "/ && $0 !~ "\"id\": \"" id "\"" {exit}
  ' "${manifest}"
}

need_base() {
  local id=$1 base=$2
  if ! image_block "${id}" | grep -qE "\"base\": \"${base}\""; then
    echo "${id} must use base ${base}" >&2
    image_block "${id}" | head -n 8 >&2
    exit 1
  fi
}

need_file "${manifest}" "builder manifest template must exist"
need_file "${ROOT}/pkg/squashfs/mksquashfs.go" "shared squashfs flags must live in pkg/squashfs"
need_file "${ROOT}/builder/img/apt-slim-finish.sh" "appliance layers must share an apt/docs cleanup helper"
need_file "${ROOT}/script/report-image-sizes.sh" "smoke/build must be able to report unique layer sizes"

need "${ROOT}/pkg/squashfs/mksquashfs.go" 'zstd' \
  "image layers must be compressed with zstd"
need "${ROOT}/builder/run.go" 'squashfs\.Args' \
  "flynn-builder run must use pkg/squashfs for mksquashfs flags"
need "${ROOT}/pkg/squashfs/mksquashfs.go" 'usr/share/doc' \
  "layer squashfs must exclude documentation from the overlay diff"
need "${ROOT}/slugbuilder/artifact/main.go" 'squashfs\.Args' \
  "slugbuilder must write zstd squashfs slugs"
need "${ROOT}/tarreceive/main.go" 'squashfs\.Args' \
  "tarreceive must write zstd squashfs layers"
need "${ROOT}/builder/img/ubuntu-noble.sh" '-comp zstd' \
  "ubuntu-noble rootfs squashfs must use zstd"
need "${ROOT}/builder/img/busybox.sh" '-comp zstd' \
  "busybox rootfs squashfs must use zstd"
need "${ROOT}/build.sh" '-comp zstd' \
  "debootstrap base layer squashfs must use zstd"

need "${wrapper}" '-s -w' \
  "Go image builds must strip binaries (-s -w) without dropping version -X"
need "${wrapper}" 'pkg/version.version' \
  "stripped binaries must still embed pkg/version.version"

need_base blobstore busybox
need_base builder busybox
need_base dockerbuilder-24 ubuntu-noble
need_base tarreceive ubuntu-noble
need_base postgres ubuntu-noble
need_base host ubuntu-noble

if image_block dockerbuilder-24 | grep -q 'heroku-24-build'; then
  echo "dockerbuilder-24 must not inherit heroku-24-build" >&2
  exit 1
fi

need "${ROOT}/builder/ubuntu-setup.sh" '--no-install-recommends' \
  "ubuntu-noble setup must not install APT Recommends"
need "${ROOT}/builder/ubuntu-setup.sh" 'snapd' \
  "ubuntu-noble setup must purge cloud image packages such as snapd"
need "${ROOT}/builder/ubuntu-setup.sh" 'cloud-init' \
  "ubuntu-noble setup must purge cloud-init"
if grep -qE 'net-tools \\' "${ROOT}/builder/ubuntu-setup.sh"; then
  echo "ubuntu-noble must not install net-tools (iproute2 is enough)" >&2
  exit 1
fi

need "${ROOT}/host/img/packages.sh" 'libseccomp2' \
  "host runtime image must install libseccomp2, not the -dev headers"
need "${ROOT}/host/img/packages.sh" 'CRYPTSETUP=n' \
  "host image must skip cryptsetup initramfs probes on overlay builder roots"
need "${ROOT}/host/img/packages.sh" 'FSTYPE=9p' \
  "host image must tell the fsck hook the VM root is 9p, not overlay"
if grep -q 'libseccomp-dev' "${ROOT}/host/img/packages.sh"; then
  echo "host image must not install libseccomp-dev" >&2
  exit 1
fi

need "${ROOT}/../flynn-plugin-mongodb/img/packages.sh" 'mongodb-org-server' \
  "mongodb plugin must install mongod, not the full mongodb-org metapackage"
need "${ROOT}/../flynn-plugin-mongodb/img/packages.sh" 'mongodb-database-tools' \
  "mongodb plugin must keep mongodump/mongorestore"
need "${ROOT}/../flynn-plugin-mongodb/img/packages.sh" 'mongodb-mongosh' \
  "mongodb plugin must keep mongosh"
if grep -qE 'apt-get install -y mongodb-org[^-]' "${ROOT}/../flynn-plugin-mongodb/img/packages.sh"; then
  echo "mongodb plugin must not install the mongodb-org metapackage" >&2
  exit 1
fi

need "${ROOT}/appliance/postgresql/img/packages.sh" 'postgresql-16-postgis-3' \
  "postgres slim-down must keep PostGIS (advertised extension)"
need "${ROOT}/appliance/postgresql/img/packages.sh" 'timescaledb-2-postgresql-16' \
  "postgres slim-down must keep TimescaleDB"
need "${ROOT}/appliance/postgresql/img/packages.sh" 'timescaledb-tools' \
  "postgres must install timescaledb-tools (timescaledb-tune; not a Recommends)"
if grep -qE 'timescaledb-tune --yes' "${ROOT}/appliance/postgresql/img/packages.sh"; then
  echo "postgres image must not run timescaledb-tune (Flynn writes postgresql.conf)" >&2
  exit 1
fi
need "${ROOT}/appliance/postgresql/process.go" 'timescaledb.max_background_workers' \
  "Flynn postgresql.conf must set timescaledb.max_background_workers"
need "${ROOT}/appliance/postgresql/img/packages.sh" 'postgresql-16-pgrouting' \
  "postgres slim-down must keep pgRouting"
if grep -q 'software-properties-common' "${ROOT}/appliance/postgresql/img/packages.sh"; then
  echo "postgres image must not install unused software-properties-common" >&2
  exit 1
fi

need "${ROOT}/../flynn-plugin-kafka/img/packages.sh" 'site-docs' \
  "kafka plugin must delete site-docs from the upstream tarball"

for pkg in \
  "${ROOT}/appliance/postgresql/img/packages.sh" \
  "${ROOT}/host/img/packages.sh" \
  "${ROOT}/gitreceive/img/packages.sh"
do
  need "${pkg}" '--no-install-recommends' \
    "$(basename "$(dirname "$(dirname "${pkg}")")") packages must pass --no-install-recommends"
  need "${pkg}" 'apt-slim-finish.sh' \
    "${pkg} must run the shared apt/docs cleanup helper"
done

redis_pkg="${ROOT}/../flynn-plugin-redis/img/packages.sh"
if [[ -f "${redis_pkg}" ]]; then
  need "${redis_pkg}" '--no-install-recommends' \
    "redis plugin packages must pass --no-install-recommends"
  need "${redis_pkg}" 'apt-slim-finish.sh' \
    "redis plugin packages must run the shared apt/docs cleanup helper"
elif [[ -f "${ROOT}/appliance/redis/img/packages.sh" ]]; then
  echo "redis still lives in Flynn; extract it or point this check at ../flynn-plugin-redis" >&2
  exit 1
fi

mariadb_pkg="${ROOT}/../flynn-plugin-mariadb/img/packages.sh"
if [[ -f "${mariadb_pkg}" ]]; then
  need "${mariadb_pkg}" '--no-install-recommends' \
    "mariadb plugin packages must pass --no-install-recommends"
  need "${mariadb_pkg}" 'apt-slim-finish.sh' \
    "mariadb plugin packages must run the shared apt/docs cleanup helper"
elif [[ -f "${ROOT}/appliance/mariadb/img/packages.sh" ]]; then
  echo "mariadb still lives in Flynn; extract it or point this check at ../flynn-plugin-mariadb" >&2
  exit 1
fi

mongodb_pkg="${ROOT}/../flynn-plugin-mongodb/img/packages.sh"
if [[ -f "${mongodb_pkg}" ]]; then
  need "${mongodb_pkg}" '--no-install-recommends' \
    "mongodb plugin packages must pass --no-install-recommends"
  need "${mongodb_pkg}" 'apt-slim-finish.sh' \
    "mongodb plugin packages must run the shared apt/docs cleanup helper"
elif [[ -f "${ROOT}/appliance/mongodb/img/packages.sh" ]]; then
  echo "mongodb still lives in Flynn; extract it or point this check at ../flynn-plugin-mongodb" >&2
  exit 1
fi

kafka_pkg="${ROOT}/../flynn-plugin-kafka/img/packages.sh"
if [[ -f "${kafka_pkg}" ]]; then
  need "${kafka_pkg}" '--no-install-recommends' \
    "kafka plugin packages must pass --no-install-recommends"
  need "${kafka_pkg}" 'apt-slim-finish.sh' \
    "kafka plugin packages must run the shared apt/docs cleanup helper"
elif [[ -f "${ROOT}/appliance/kafka/img/packages.sh" ]]; then
  echo "kafka still lives in Flynn; extract it or point this check at ../flynn-plugin-kafka" >&2
  exit 1
fi

clickhouse_pkg="${ROOT}/../flynn-plugin-clickhouse/img/packages.sh"
if [[ -f "${clickhouse_pkg}" ]]; then
  need "${clickhouse_pkg}" '--no-install-recommends' \
    "clickhouse plugin packages must pass --no-install-recommends"
  need "${clickhouse_pkg}" 'apt-slim-finish.sh' \
    "clickhouse plugin packages must run the shared apt/docs cleanup helper"
elif [[ -f "${ROOT}/appliance/clickhouse/img/packages.sh" ]]; then
  echo "clickhouse still lives in Flynn; extract it or point this check at ../flynn-plugin-clickhouse" >&2
  exit 1
fi

need "${ROOT}/../flynn-plugin-clickhouse/img/packages.sh" 'libcap2-bin' \
  "clickhouse plugin must still install libcap2-bin long enough to clear file caps"
need "${ROOT}/../flynn-plugin-clickhouse/img/packages.sh" 'purge' \
  "clickhouse plugin must purge libcap2-bin after setcap"

need "${ROOT}/builder/img/go.sh" 'go/test' \
  "Go toolchain image must drop GOROOT test/doc trees"

# Live smoke must exercise the slimmed runtime, not only static package lists.
need "${smoke}" 'pg_available_extensions' \
  "smoke must verify postgres still ships postgis/pgrouting/timescaledb"
need "${smoke}" 'mongodb dump' \
  "smoke must run flynn mongodb dump against the slimmed mongodump tools"
need "${smoke}" 'blobstore.discoverd/.well-known/status' \
  "smoke must hit blobstore status after moving it to busybox"
need "${ROOT}/dockerbuilder/img/packages.sh" 'runc' \
  "dockerbuilder on ubuntu-noble must install runc for BuildKit's OCI worker"
need "${smoke}" 'step_deploy_docker_app' \
  "smoke must git-push a Dockerfile app through slimmed dockerbuilder-24"
need "${smoke}" 'docker-http' \
  "smoke must HTTP-probe the Dockerfile app after moving dockerbuilder off heroku-24-build"
need "${smoke}" 'docker-cli-run' \
  "smoke must flynn run against the Dockerfile app (container image, not slugrunner)"
need "${smoke}" './pkg/dockerimage/' \
  "host unit gate must compile dockerimage container-stack release tests"
need "${smoke}" './pkg/netpolicy/' \
  "host unit gate must compile netpolicy isolation tests"
need "${smoke}" 'flynn-host version' \
  "smoke must run flynn-host version (stripped cgo binary + libseccomp2)"
need "${smoke}" 'report-image-sizes.sh' \
  "smoke build step must print unique squashfs layer sizes"
need "${smoke}" './pkg/squashfs/' \
  "host unit gate must compile pkg/squashfs tests"
need "${smoke}" './builder/' \
  "Linux host/builder unit gate must compile builder mksquashfs tests"

echo "ok image slim-down regressions (bases, apt, zstd, strip, live probes)"

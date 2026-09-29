#!/bin/bash
#
# Vagrant Flynn smoke test. This is the pre-release gate: it builds Flynn on a
# Vagrant builder VM, then boots real clusters in every requested topology and
# exercises deploys, datastores, CLI, volumes, upgrades, membership changes and
# backup/restore. It cannot run in GitHub Actions (needs nested VMs), and by the
# time production would tell us it is too late, so run it locally before
# cutting a release.
#
# The implementation still lives in script/vagrant/suite.sh (the name
# predates the test growing past upgrades); every option, environment variable
# and companion contract test documented there applies here unchanged.
#
#   script/vagrant-smoke.sh                    # enabled items in smoke-matrix.yaml
#   script/vagrant-smoke.sh --list             # show matrix items (no VMs)
#   script/vagrant-smoke.sh --item quick       # contributor default (boot + git/docker; no tenant postgres)
#   script/vagrant-smoke.sh --item minio       # S3-compatible blobstore (MinIO) + mysql backup
#   script/vagrant-smoke.sh --item pipeline    # pipeline create/add/promote into an empty prod app
#   script/vagrant-smoke.sh --item datastores  # 1-node then 3-node: plugins, resource:add, seed, follow, teardown
#   script/vagrant-smoke.sh --item singleton   # one named configuration
#   SMOKE_TOPOLOGIES=3 script/vagrant-smoke.sh # env still overrides topologies
#   SKIP_BUILD=1 script/vagrant-smoke.sh       # reuse the last tarball
#   script/vagrant-smoke.sh --item singleton --datastore postgres
#   script/vagrant-smoke.sh --item singleton --datastore redis --datastore kafka
#
# VM lifecycle (does not run the suite; .vagrant only, never .vagrant-dev):
#   script/vagrant-smoke.sh status|ssh|up|reload|restart|stop|halt|destroy|teardown
#   script/vagrant-smoke.sh env                # same commands; env help
#   make vagrant-smoke                         # --item quick
#   make vagrant-smoke-status|vagrant-smoke-stop|vagrant-smoke-destroy
#
# Incremental rebuilds: build.sh keeps the image layer cache and a shared Go
# build cache on the builder between runs, so after a fix only the layers whose
# inputs changed are rebuilt (see docs/content/development.html.md).
#
# Laptop loop (separate runtime): script/vagrant.sh / make vagrant-setup
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "${1:-}" in
  status|ssh|up|reload|restart|stop|halt|destroy|teardown|env)
    if [[ "${1}" == "env" ]]; then
      shift
    fi
    exec "${HERE}/vagrant/smoke-env.sh" "$@"
    ;;
esac
exec "${HERE}/vagrant/suite.sh" "$@"

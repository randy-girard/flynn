#!/bin/bash
# Laptop Vagrant loop. Implementation lives in script/vagrant/.
# Smoke is script/vagrant-smoke.sh so the two running envs never mix.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "${HERE}/vagrant/vagrant.sh" "$@"

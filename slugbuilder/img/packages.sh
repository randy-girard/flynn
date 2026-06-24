#!/bin/bash

cp -r slugbuilder/builder /builder

# Explicitly number the buildpacks directory based on the order of buildpacks.txt
nl -nrz /builder/buildpacks.txt | awk '{print $2 "\t" $1}' | xargs -L 1 /builder/install-buildpack /builder/buildpacks

# Heroku Go buildpack (and any buildpack using the same bootstrap) downloads
# jq-linux64 from Heroku storage — amd64 only. Replace lib/common_tools.sh with a
# Flynn variant that seeds ${cache}/.jq/bin/jq from the distro jq (multi-arch).
if [[ -f /builder/common_tools.flynn.sh ]]; then
  while IFS= read -r -d '' f; do
    if grep -q 'jq-linux64' "${f}" 2>/dev/null; then
      cp /builder/common_tools.flynn.sh "${f}"
    fi
  done < <(find /builder/buildpacks -path '*/lib/common_tools.sh' -print0)
fi

chmod +x /builder/patch-heroku-go-multiarch.sh 2>/dev/null || true
if [[ -x /builder/patch-heroku-go-multiarch.sh ]]; then
  /builder/patch-heroku-go-multiarch.sh /builder/flynn-heroku-go-common-overrides.sh
fi

# allow custom buildpack install by unprivileged user
chmod ugo+w /builder/buildpacks

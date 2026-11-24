#!/bin/bash

if [[ -z "${HOME}" ]] || [[ "${HOME}" == "/" ]] ; then
  export HOME=/root
fi

# The cluster SSH client key is only for apps the operator opted in
# (SSH_CLIENT_KEY_APPS, BUILD-08 / EDGE-02). It is never written to disk here:
# the submodule fetch stages it in a 0600 tmpfs file for that fetch only and
# removes it afterwards (EDGE-26). With no app opted in, drop it entirely.
if [[ -z "${SSH_CLIENT_KEY_APPS}" ]]; then
  unset SSH_CLIENT_KEY
fi

# If there is a list of known SSH hosts available in the environment, save it so that it can be used
if [[ -n "${SSH_CLIENT_HOSTS}" ]]; then
  mkdir -p ${HOME}/.ssh
  file="${HOME}/.ssh/known_hosts"
  echo "${SSH_CLIENT_HOSTS}" > ${file}
  chmod 600 ${file}
fi

exec "${GITRECEIVED:-/bin/gitreceived}"

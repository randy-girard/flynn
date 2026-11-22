#!/bin/bash

if [[ -z "${HOME}" ]] || [[ "${HOME}" == "/" ]] ; then
  export HOME=/root
fi

# The cluster SSH client key is only for apps the operator opted in
# (SSH_CLIENT_KEY_APPS, BUILD-08 / EDGE-02). It is written to a named file
# that the submodule fetch passes to ssh explicitly, not to ~/.ssh/id_rsa, so
# no other ssh or git in this container uses it by default.
if [[ -n "${SSH_CLIENT_KEY}" ]] && [[ -n "${SSH_CLIENT_KEY_APPS}" ]]; then
  mkdir -p "${HOME}/.ssh"
  chmod 700 "${HOME}/.ssh"
  file="${HOME}/.ssh/cluster_client_key"
  (umask 077 && printf '%s\n' "${SSH_CLIENT_KEY}" > "${file}")
  chmod 600 "${file}"
fi

# If there is a list of known SSH hosts available in the environment, save it so that it can be used
if [[ -n "${SSH_CLIENT_HOSTS}" ]]; then
  mkdir -p ${HOME}/.ssh
  file="${HOME}/.ssh/known_hosts"
  echo "${SSH_CLIENT_HOSTS}" > ${file}
  chmod 600 ${file}
fi

exec /bin/gitreceived

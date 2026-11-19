# SSH client key helpers, sourced by build.sh (BUILD-08).
#
# SSH_CLIENT_KEY is a cluster-wide private key. It is only forwarded to apps
# the operator opted in (gitreceive SSH_CLIENT_KEY_APPS), and even then
# buildpack code, which runs as the unprivileged build user, must never be able
# to read it. Root stages the key in a root-only file, loads it into an
# ssh-agent that the build user can talk to, and deletes the file. The agent
# signs on behalf of the build user; it never hands the key out and it refuses
# ptrace/core dumps of its memory.

SSH_SECRETS_DIR="${SSH_SECRETS_DIR:-/run/secrets}"
SSH_AGENT_DIR="${SSH_AGENT_DIR:-/run/ssh-agent}"
SSH_CLIENT_KEY_FILE="${SSH_SECRETS_DIR}/ssh_client_key"

# ssh_client_key_stage moves SSH_CLIENT_KEY out of the environment into a
# root-owned 0600 file, and writes the (public) known_hosts for the build user.
ssh_client_key_stage() {
  if [[ -n "${SSH_CLIENT_KEY}" ]]; then
    mkdir -p "${SSH_SECRETS_DIR}"
    (
      umask 077
      printf '%s\n' "${SSH_CLIENT_KEY}" > "${SSH_CLIENT_KEY_FILE}"
    )
    chmod 600 "${SSH_CLIENT_KEY_FILE}"
  fi
  unset SSH_CLIENT_KEY

  if [[ -n "${SSH_CLIENT_HOSTS}" ]]; then
    mkdir -p "${HOME}/.ssh"
    printf '%s\n' "${SSH_CLIENT_HOSTS}" > "${HOME}/.ssh/known_hosts"
    chmod 644 "${HOME}/.ssh/known_hosts"
    if [[ "$(id -u)" == "0" && -n "${USER}" ]]; then
      chown -R "${USER}:${USER}" "${HOME}/.ssh"
    fi
  fi
  unset SSH_CLIENT_HOSTS
}

# ssh_client_key_load starts an ssh-agent for the build user, loads the staged
# key as root and deletes the key file. SSH_AUTH_SOCK is exported for the
# buildpack. No-op when no key was staged.
ssh_client_key_load() {
  [[ -f "${SSH_CLIENT_KEY_FILE}" ]] || return 0
  mkdir -p "${SSH_AGENT_DIR}"
  chmod 700 "${SSH_AGENT_DIR}"
  local sock="${SSH_AGENT_DIR}/agent.sock"
  local runner=""
  if [[ "$(id -u)" == "0" && -n "${USER}" ]]; then
    chown "${USER}:${USER}" "${SSH_AGENT_DIR}"
    runner="setuidgid ${USER}"
  fi
  # The agent runs as the build user because ssh-agent only serves peers with
  # its own uid (or root). root can still add the key below.
  local agent_out
  agent_out=$(${runner} ssh-agent -s -a "${sock}") || return 1
  eval "${agent_out}" > /dev/null
  export SSH_AUTH_SOCK SSH_AGENT_PID
  if ! ssh-add "${SSH_CLIENT_KEY_FILE}" > /dev/null 2>&1; then
    rm -f "${SSH_CLIENT_KEY_FILE}"
    echo "failed to load SSH client key into ssh-agent" >&2
    return 1
  fi
  rm -f "${SSH_CLIENT_KEY_FILE}"
}

# ssh_client_key_cleanup stops the agent and removes everything it left behind.
ssh_client_key_cleanup() {
  if [[ -n "${SSH_AGENT_PID}" ]]; then
    kill "${SSH_AGENT_PID}" 2> /dev/null || true
  fi
  unset SSH_AGENT_PID SSH_AUTH_SOCK
  rm -f "${SSH_CLIENT_KEY_FILE}" "${HOME}/.ssh/known_hosts"
  rm -rf "${SSH_AGENT_DIR}"
}

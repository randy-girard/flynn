#!/bin/bash
# GitHub-hosted Ubuntu runners list azure.archive.ubuntu.com in
# /etc/apt/apt-mirrors.txt. When that name does not resolve, apt-get spends
# its retries there and the job ends before any test runs. archive.ubuntu.com
# is the public mirror those runners can still reach.

flynn_ci_use_public_ubuntu_mirror() {
  flynn_ci_set_ubuntu_mirror archive.ubuntu.com "$@"
}

# flynn_ci_ubuntu_mirrors is tried in order. archive.ubuntu.com is the usual
# public mirror. The others are used when that name does not resolve on a
# GitHub-hosted runner.
flynn_ci_ubuntu_mirrors() {
  printf '%s\n' \
    archive.ubuntu.com \
    mirrors.edge.kernel.org \
    mirrors.ocf.berkeley.edu
}

# flynn_ci_set_ubuntu_mirror points every Ubuntu archive host in the given
# files at host. azure.archive.ubuntu.com is replaced before archive.ubuntu.com
# so the azure name is not left with a new suffix.
flynn_ci_set_ubuntu_mirror() {
  local host="$1"
  shift
  local f tmp
  for f in "$@"; do
    [[ -f "${f}" ]] || continue
    tmp="$(mktemp)"
    sed \
      -e "s/azure\\.archive\\.ubuntu\\.com/${host}/g" \
      -e "s/security\\.ubuntu\\.com/${host}/g" \
      -e "s/archive\\.ubuntu\\.com/${host}/g" \
      -e "s/mirrors\\.edge\\.kernel\\.org/${host}/g" \
      -e "s/mirrors\\.ocf\\.berkeley\\.edu/${host}/g" \
      "${f}" > "${tmp}"
    cat "${tmp}" > "${f}"
    rm -f "${tmp}"
  done
}

# flynn_ci_apt_install updates apt and installs packages, trying the next
# mirror when the current one cannot be resolved. A short acquire timeout
# keeps a dead mirror from using the whole job.
flynn_ci_apt_install() {
  cat > /etc/apt/apt.conf.d/99flynn-ci-timeout <<'EOF'
Acquire::http::Timeout "20";
Acquire::https::Timeout "20";
Acquire::ftp::Timeout "20";
Acquire::Retries "1";
EOF
  local files=(/etc/apt/apt-mirrors.txt /etc/apt/sources.list)
  local extra host rc=1
  shopt -s nullglob
  for extra in /etc/apt/sources.list.d/*; do
    files+=("${extra}")
  done
  shopt -u nullglob
  while IFS= read -r host; do
    [[ -n "${host}" ]] || continue
    flynn_ci_set_ubuntu_mirror "${host}" "${files[@]}"
    if apt-get update && apt-get install -y "$@"; then
      return 0
    fi
    rc=$?
  done < <(flynn_ci_ubuntu_mirrors)
  return "${rc}"
}

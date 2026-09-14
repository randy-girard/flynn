#!/bin/bash
# Retry apt-get on transient mirror / TLS / lock failures.
# Source from host scripts (setup.sh, build.sh, apparmor). Image jobs use the
# flynn-apt-get PATH shim plus flynnAptLayerPrelude.

flynn_apt_install_conf() {
  # apt-key / _apt mkstemp under /tmp; 0755 (or a wiped tmpdir) yields
  # "Couldn't create temporary file /tmp/apt.conf.*".
  mkdir -p /tmp
  chmod 1777 /tmp 2>/dev/null || true
  mkdir -p /etc/apt/apt.conf.d
  cat >/etc/apt/apt.conf.d/80-flynn-retries <<'EOF'
Acquire::Retries "5";
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
Acquire::ForceIPv4 "true";
EOF
}

# Builder VMs add docker.com / mongodb as host apt sources. Those URLs must not
# appear in image chroots (shared _apt_lists is bind-mounted; a leaked
# docker.sources makes `apt-get update --error-on=any` fail the whole build).
flynn_apt_strip_third_party_sources() {
  rm -f /etc/apt/sources.list.d/docker.sources \
    /etc/apt/sources.list.d/docker.list \
    /etc/apt/sources.list.d/mongodb-org-8.0.list \
    /etc/apt/sources.list.d/mongodb-org-*.list 2>/dev/null || true
}

flynn_apt_transient_pattern() {
  echo 'Failed to fetch|Hash Sum mismatch|Temporary failure resolving|Connection timed out|Could not resolve|Network is unreachable|502 Bad Gateway|503 Service|download.docker.com|Unable to lock|Could not get lock|I/O error|Connection reset|TLS handshake|the remote end hung up| 504 | 522 |Clearing|Splitting up'
}

# Disable docker.com / mongodb lists so Ubuntu package installs survive a
# third-party mirror blip. Callers restore the files afterward.
flynn_apt_stash_third_party_sources() {
  local f
  FLYNN_APT_STASHED=()
  for f in /etc/apt/sources.list.d/docker.sources \
    /etc/apt/sources.list.d/docker.list \
    /etc/apt/sources.list.d/mongodb-org-8.0.list; do
    if [[ -e "${f}" ]]; then
      mv "${f}" "${f}.flynn-apt-bak"
      FLYNN_APT_STASHED+=("${f}")
    fi
  done
}

flynn_apt_restore_third_party_sources() {
  local f
  for f in "${FLYNN_APT_STASHED[@]:-}"; do
    [[ -e "${f}.flynn-apt-bak" ]] && mv "${f}.flynn-apt-bak" "${f}" || true
  done
  FLYNN_APT_STASHED=()
}

flynn_apt_cmd() {
  local max="${FLYNN_APT_RETRIES:-5}"
  local n=1
  local delay=5
  local rc=0
  flynn_apt_install_conf
  while true; do
    if apt-get "$@"; then
      return 0
    fi
    rc=$?
    if [[ "${n}" -ge "${max}" ]]; then
      break
    fi
    echo "apt-get $* failed (attempt ${n}/${max}, rc=${rc}); retrying in ${delay}s..." >&2
    rm -rf /var/lib/apt/lists/partial/* /var/cache/apt/archives/partial/* 2>/dev/null || true
    if [[ "${n}" -ge 2 ]]; then
      rm -f /var/lib/apt/lists/*InRelease /var/lib/apt/lists/*_InRelease 2>/dev/null || true
    fi
    sleep "${delay}"
    n=$((n + 1))
    delay=$((delay * 2))
    if [[ "${delay}" -gt 45 ]]; then
      delay=45
    fi
  done
  return "${rc}"
}

# Host apt-get update: retry, then drop docker.com/mongodb if they are still
# the only thing failing. Flynn runtime deps (iptables/zfs) come from Ubuntu.
flynn_apt_update_host() {
  if flynn_apt_cmd update "$@"; then
    return 0
  fi
  echo "apt-get update still failing; retrying without docker.com/mongodb sources" >&2
  flynn_apt_stash_third_party_sources
  local rc=0
  apt-get update "$@" || rc=$?
  flynn_apt_restore_third_party_sources
  return "${rc}"
}

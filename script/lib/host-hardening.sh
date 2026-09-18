#!/bin/bash
# Flynn host firewall + logrotate helpers. Safe to source; defines functions only.
# Public ingress is SSH/HTTP/HTTPS. RFC1918 + CGNAT stay open so cluster peers
# can still reach host APIs after the public default-deny.

flynn_firewall_public_tcp_ports() {
  echo "22 80 443"
}

flynn_firewall_cluster_cidrs() {
  echo "10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10"
}

flynn_logrotate_config() {
  cat <<'EOF'
# Flynn host and job logs. copytruncate avoids interrupting writers that keep
# /var/log/flynn/*.log open (flynn-host, logsink).
/var/log/flynn/*.log
/var/log/flynn/*/*.log {
    daily
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    sharedscripts
}
EOF
}

_flynn_harden_msg() {
  if declare -F info >/dev/null 2>&1; then
    info "$1"
  else
    echo "===> $1"
  fi
}

_flynn_harden_warn() {
  if declare -F warn >/dev/null 2>&1; then
    warn "$1"
  else
    echo "===> WARN: $1" >&2
  fi
}

configure_flynn_logrotate() {
  local dest_dir="${FLYNN_LOGROTATE_DIR:-/etc/logrotate.d}"
  local log_dir="${FLYNN_LOG_DIR:-/var/log/flynn}"
  _flynn_harden_msg "configuring logrotate for ${log_dir}"
  mkdir -p "${dest_dir}" "${log_dir}"
  flynn_logrotate_config > "${dest_dir}/flynn"
  chmod 0644 "${dest_dir}/flynn"
}

flynn_firewall_plan() {
  echo "default deny incoming"
  echo "default allow outgoing"
  local port
  for port in $(flynn_firewall_public_tcp_ports); do
    echo "allow ${port}/tcp"
  done
  local cidr
  for cidr in $(flynn_firewall_cluster_cidrs); do
    echo "allow from ${cidr}"
  done
}

configure_flynn_firewall() {
  _flynn_harden_msg "configuring host firewall (public TCP 22, 80, 443; private cluster CIDRs)"

  if [[ "${FLYNN_FIREWALL_DRY_RUN:-}" == "1" ]]; then
    flynn_firewall_plan
    return 0
  fi

  if ! command -v ufw >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get install --yes ufw
    else
      _flynn_harden_warn "ufw is not installed; skipping Flynn host firewall"
      return 0
    fi
  fi

  # Do not `ufw reset`: preserve operator-added rules. Default deny + explicit
  # allows is the Flynn public surface (22/80/443) plus private cluster peers.
  ufw default deny incoming
  ufw default allow outgoing

  local port
  for port in $(flynn_firewall_public_tcp_ports); do
    ufw allow "${port}/tcp" comment "flynn-public"
  done

  local cidr
  for cidr in $(flynn_firewall_cluster_cidrs); do
    ufw allow from "${cidr}" comment "flynn-cluster"
  done

  ufw --force enable
}

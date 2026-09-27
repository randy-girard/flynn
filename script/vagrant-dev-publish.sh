#!/bin/bash
# Publish the in-VM cluster address (192.0.2.200) on the Vagrant host-only
# NIC so the laptop can open the router. bootstrap-flynn binds the router to
# that address only.
set -euo pipefail
nic="$(ip -4 -o addr show | awk '$4 ~ /^192\.168\.(56|57)\./ {print $2; exit}')"
if [[ -z "${nic}" ]]; then
  echo "no 192.168.56/57 host-only interface; skipping publish" >&2
  exit 0
fi
sysctl -w net.ipv4.conf.all.rp_filter=2 >/dev/null
sysctl -w "net.ipv4.conf.${nic}.rp_filter=2" >/dev/null
if ! iptables -t nat -C PREROUTING -i "${nic}" -p tcp -m multiport --dports 80,443,3000:3500 -j DNAT --to-destination 192.0.2.200 2>/dev/null; then
  iptables -t nat -A PREROUTING -i "${nic}" -p tcp -m multiport --dports 80,443,3000:3500 -j DNAT --to-destination 192.0.2.200
fi
echo "published 192.0.2.200 via ${nic}"

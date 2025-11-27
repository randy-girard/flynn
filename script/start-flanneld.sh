#!/bin/bash
set -e

PIDDIR="/var/run/flynn-local"
mkdir -p "$PIDDIR"

flanneld \
  -discoverd-url="http://${DISCOVERD}" \
  -iface="${EXTERNAL_IP}" \
  -http-port="5001" \
  -notify-url="http://${EXTERNAL_IP}:1113/host/network" \
  -logtostderr \
  > /tmp/flanneld.log 2>&1 &
echo $! > "$PIDDIR/flanneld.pid"

EXTERNAL_IP=192.0.2.200 \
  PORT=5002 \
  NETWORK=100.100.0.0/16 \
  flannel-wrapper \
  > /tmp/flanneld-wrapper.log 2>&1 &
echo $! > "$PIDDIR/flanneld-wrapper.pid"

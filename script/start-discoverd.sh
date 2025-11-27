#!/bin/bash
set -e

PIDDIR="/var/run/flynn-local"
mkdir -p "$PIDDIR"

discoverd \
  -data-dir=/tmp/discoverd-data \
  -host=192.0.2.200 \
  -addr=192.0.2.200:1111 \
  -notify="http://192.0.2.200:1113/host/discoverd" \
  -wait-net-dns=true \
  > /tmp/discoverd.log 2>&1 &
echo $! > "$PIDDIR/discoverd.pid"

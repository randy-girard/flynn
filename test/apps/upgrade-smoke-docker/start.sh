#!/bin/sh
# Tiny HTTP server for the smoke Dockerfile deploy. Listens on $PORT (Flynn).
set -eu
port="${PORT:-8080}"
mkdir -p /www
printf '%s\n' "${SMOKE_BODY:-docker-smoke ok}" > /www/index.html
exec httpd -f -p "${port}" -h /www

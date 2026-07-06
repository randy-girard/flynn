#!/bin/bash

case $1 in
  mariadb)
    mkdir -p /data/tmp
    chown -R mysql:mysql /data
    chmod 0700 /data
    chmod 0700 /data/tmp
    shift
    exec sudo \
      -u mysql \
      -E -H \
      /bin/flynn-mariadb $*
    ;;
  api)
    shift
    exec /bin/flynn-mariadb-api $*
    ;;
  *)
    echo "Usage: $0 {mariadb|api}"
    exit 2
    ;;
esac

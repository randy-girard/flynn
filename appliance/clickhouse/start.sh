#!/bin/bash

case $1 in
  keeper)
    shift
    exec /bin/flynn-clickhouse keeper $*
    ;;
  clickhouse)
    shift
    exec /bin/flynn-clickhouse $*
    ;;
  api)
    shift
    exec /bin/flynn-clickhouse-api $*
    ;;
  *)
    echo "Usage: $0 {keeper|clickhouse|api}"
    exit 2
    ;;
esac

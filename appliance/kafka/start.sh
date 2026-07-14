#!/bin/bash

case $1 in
  kafka)
    shift
    exec /bin/flynn-kafka $*
    ;;
  api)
    shift
    exec /bin/flynn-kafka-api $*
    ;;
  *)
    echo "Usage: $0 {kafka|api}"
    exit 2
    ;;
esac

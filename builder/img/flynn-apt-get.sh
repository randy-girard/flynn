#!/bin/bash
# Retry apt-get update/install/upgrade on transient mirror failures.
# Installed as /mnt/bin/apt-get ahead of /usr/bin/apt-get in flynn-builder jobs.
set -u

REAL="${REAL_APT_GET:-/usr/bin/apt-get}"
if [[ ! -x "${REAL}" ]]; then
  REAL="$(command -v apt-get 2>/dev/null || true)"
fi
if [[ -z "${REAL}" || "${REAL}" == "$0" ]]; then
  echo "flynn-apt-get: system apt-get not found" >&2
  exit 127
fi

should_retry=0
for a in "$@"; do
  case "${a}" in
    update | install | upgrade | dist-upgrade | build-dep)
      should_retry=1
      ;;
  esac
done

if [[ "${should_retry}" -eq 0 ]]; then
  exec "${REAL}" "$@"
fi

max="${FLYNN_APT_RETRIES:-5}"
n=1
delay=5
rc=0
while true; do
  if "${REAL}" "$@"; then
    exit 0
  fi
  rc=$?
  if [[ "${n}" -ge "${max}" ]]; then
    exit "${rc}"
  fi
  echo "flynn-apt-get: $* failed (attempt ${n}/${max}, rc=${rc}); retrying in ${delay}s" >&2
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

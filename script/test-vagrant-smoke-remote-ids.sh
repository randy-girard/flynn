#!/bin/bash
# Regression test: concurrent remote script IDs must not collide.
# The smoke failure on 2026-09-09 was caused by a fixed remote-node1.sh name
# shared by bootstrap and the overlay watcher.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/smoke-remote-id.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

# Mirror the production naming scheme (BASHPID + seq + random + time).
# BASHPID (not $$) differs in background subshells such as the overlay watcher.
make_id() {
  local node=$1 seq=$2
  local pid="${BASHPID:-$(sh -c 'echo $PPID')}"
  echo "${node}.${pid}.${seq}.${RANDOM}.$(date +%s)"
}

# Single-process uniqueness with an explicit counter (avoids subshell reset).
# No associative arrays: must also run under macOS /bin/bash 3.2 like the smoke.
seq=0
: > "${TMP}/single.txt"
for _ in $(seq 1 500); do
  seq=$((seq + 1))
  make_id node1 "${seq}" >> "${TMP}/single.txt"
done
if [[ -n "$(sort "${TMP}/single.txt" | uniq -d)" ]]; then
  echo "duplicate id in-process:" >&2
  sort "${TMP}/single.txt" | uniq -d >&2
  exit 1
fi

# Cross-process uniqueness: two concurrent subshells (as bootstrap + overlay
# watcher are in the smoke script) must never choose the same basename. Both
# deliberately restart their seq counter at 1 and share $$, so only BASHPID
# keeps them apart.
(
  for i in $(seq 1 100); do
    make_id node1 "${i}" >> "${TMP}/a.txt"
  done
) &
(
  for i in $(seq 1 100); do
    make_id node1 "${i}" >> "${TMP}/b.txt"
  done
) &
wait
cat "${TMP}/a.txt" "${TMP}/b.txt" | sort | uniq -d > "${TMP}/dupes.txt"
if [[ -s "${TMP}/dupes.txt" ]]; then
  echo "cross-process duplicate ids:" >&2
  cat "${TMP}/dupes.txt" >&2
  exit 1
fi

# Grep the smoke script for the unique-id pattern (not the old fixed name).
if grep -n 'script_name="remote-${node}.sh"' "${ROOT}/script/vagrant-upgrade-smoke.sh"; then
  echo "smoke script still uses fixed remote-\${node}.sh names" >&2
  exit 1
fi
if ! grep -q 'pid="\${BASHPID:-\$(sh -c .echo \$PPID.)}"' "${ROOT}/script/vagrant-upgrade-smoke.sh" \
  || ! grep -q 'id="\${node}\.\${pid}\.\${REMOTE_SCRIPT_SEQ}\.\${RANDOM}' "${ROOT}/script/vagrant-upgrade-smoke.sh"; then
  echo "smoke script must build remote script ids from subshell pid (BASHPID w/ bash3 fallback)+seq+RANDOM; \$\$ collides across subshells" >&2
  exit 1
fi

if grep -n 'vagrant ssh "${node}"' "${ROOT}/script/vagrant-upgrade-smoke.sh"; then
  echo "smoke script must not call vagrant ssh per node (machine lock races concurrent overlay watcher)" >&2
  exit 1
fi
if ! grep -q 'ssh -F "${cfg}"' "${ROOT}/script/vagrant-upgrade-smoke.sh"; then
  echo "smoke script must ssh via cached vagrant ssh-config (ssh -F)" >&2
  exit 1
fi

echo "ok remote script id uniqueness + direct ssh (no vagrant machine lock)"

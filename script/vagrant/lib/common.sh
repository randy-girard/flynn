#!/bin/bash
# Shared paths and Vagrant helpers for the laptop loop and smoke.
# Runtime state stays isolated; this file is only code.

if [[ -z "${FLYNN_VAGRANT_LIB_DIR:-}" ]]; then
  FLYNN_VAGRANT_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
FLYNN_VAGRANT_DIR="$(cd "${FLYNN_VAGRANT_LIB_DIR}/.." && pwd)"
if [[ -z "${FLYNN_ROOT:-}" ]]; then
  FLYNN_ROOT="$(cd "${FLYNN_VAGRANT_DIR}/../.." && pwd)"
fi
ROOT="${FLYNN_ROOT}"
SRC="/root/go/src/github.com/flynn/flynn"
FLYNN_VAGRANT_GUEST="${FLYNN_VAGRANT_DIR}/guest"
FLYNN_VAGRANT_HOST="${FLYNN_VAGRANT_DIR}/host"

need_vagrant() {
  command -v vagrant >/dev/null 2>&1 || {
    echo "vagrant is not installed" >&2
    exit 1
  }
}

# machine_state is running, poweroff, saved, not_created, or unknown.
# Each env has its own VAGRANT_DOTFILE_PATH, so smoke and the laptop loop
# never count each other's machines.
machine_state() {
  local m="$1"
  local line
  line="$(vagrant status --machine-readable "${m}" 2>/dev/null | awk -F, -v m="${m}" '$2==m && $3=="state" {print $4; exit}')"
  if [[ -z "${line}" ]]; then
    echo "unknown"
    return
  fi
  echo "${line}"
}

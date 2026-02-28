#!/bin/bash
#
# Setup AppArmor for Flynn containers.
# This script installs the flynn-default AppArmor profile and ensures
# AppArmor is enabled. It is idempotent and safe to run multiple times.

set -e

PROFILE_NAME="flynn-default"
PROFILE_SRC="$(dirname "$0")/${PROFILE_NAME}"
PROFILE_DST="/etc/apparmor.d/${PROFILE_NAME}"

info() {
  echo "==> AppArmor: $*"
}

warn() {
  echo "==> AppArmor WARNING: $*" >&2
}

# Check if AppArmor is supported by the kernel
check_kernel_support() {
  if [[ ! -d /sys/kernel/security/apparmor ]]; then
    warn "AppArmor is not supported by this kernel. Skipping AppArmor setup."
    warn "Containers will run without AppArmor confinement."
    exit 0
  fi

  local enabled
  enabled=$(cat /sys/module/apparmor/parameters/enabled 2>/dev/null || echo "N")
  if [[ "${enabled}" != "Y" ]]; then
    warn "AppArmor is not enabled in the kernel. Skipping AppArmor setup."
    warn "To enable AppArmor, add 'apparmor=1 security=apparmor' to kernel boot parameters."
    exit 0
  fi
}

# Install AppArmor packages if not already present
install_packages() {
  if command -v apparmor_parser &>/dev/null; then
    return 0
  fi

  info "installing apparmor packages"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq apparmor apparmor-utils
}

# Install the flynn-default profile
install_profile() {
  if [[ ! -f "${PROFILE_SRC}" ]]; then
    warn "Profile source not found at ${PROFILE_SRC}"
    exit 1
  fi

  info "installing ${PROFILE_NAME} profile to ${PROFILE_DST}"
  cp "${PROFILE_SRC}" "${PROFILE_DST}"
}

# Load/reload the profile
load_profile() {
  info "loading ${PROFILE_NAME} profile"
  if ! apparmor_parser -r -W "${PROFILE_DST}" 2>/dev/null; then
    # -r fails if the profile isn't loaded yet, try without -r
    if ! apparmor_parser -a -W "${PROFILE_DST}"; then
      warn "failed to load AppArmor profile. Containers will run without AppArmor confinement."
      exit 0
    fi
  fi
  info "${PROFILE_NAME} profile loaded successfully"
}

# Verify the profile is loaded
verify_profile() {
  if [[ -f /sys/kernel/security/apparmor/profiles ]]; then
    if grep -q "${PROFILE_NAME}" /sys/kernel/security/apparmor/profiles; then
      info "${PROFILE_NAME} profile is active"
      return 0
    fi
  fi
  warn "${PROFILE_NAME} profile does not appear to be active"
  return 1
}

main() {
  check_kernel_support
  install_packages
  install_profile
  load_profile
  verify_profile
}

main "$@"


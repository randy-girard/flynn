#!/bin/bash
# Git 2.35+ refuses a repo whose .git is owned by another UID ("dubious
# ownership", exit 128). Vagrant synced folders and Docker binds do that when
# we build as root. Go 1.18+ then fails with:
#   error obtaining VCS status: exit status 128
# Flynn already stamps version via ldflags; this only lets git/go see the tree.
#
# shellcheck shell=bash

flynn_git_safe_directory() {
  local dir="${1:-}"
  if [[ -z "${dir}" ]]; then
    dir="$(pwd)"
  fi
  git config --global --add safe.directory "${dir}" 2>/dev/null || true
  # Cover vboxsf/docker mounts whose canonical path differs from dir.
  git config --global --add safe.directory '*' 2>/dev/null || true
}

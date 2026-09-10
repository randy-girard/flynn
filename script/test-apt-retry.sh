#!/bin/bash
# Retry wrapper for apt-get must retry update/install and pass through other verbs.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
shim="${ROOT}/builder/img/flynn-apt-get.sh"
lib="${ROOT}/script/lib/apt-retry.sh"

[[ -f "${shim}" ]] || { echo "missing ${shim}" >&2; exit 1; }
[[ -f "${lib}" ]] || { echo "missing ${lib}" >&2; exit 1; }
grep -q 'FLYNN_APT_RETRIES' "${shim}" || { echo "shim must honor FLYNN_APT_RETRIES" >&2; exit 1; }

fake="$(mktemp)"
trap 'rm -f "${fake}" "${fake}.n"' EXIT
cat >"${fake}" <<'EOF'
#!/bin/bash
nfile="$(dirname "$0")/$(basename "$0").n"
n=0
[[ -f "${nfile}" ]] && n="$(cat "${nfile}")"
n=$((n + 1))
echo "${n}" >"${nfile}"
echo "fake-apt $* (call ${n})" >&2
if [[ "${n}" -lt 3 ]]; then
  echo "E: Failed to fetch https://download.docker.com/linux/ubuntu noble InRelease" >&2
  exit 1
fi
exit 0
EOF
chmod +x "${fake}"
rm -f "${fake}.n"

export REAL_APT_GET="${fake}"
export FLYNN_APT_RETRIES=5
# sleep 5+10 would make this slow; stub sleep via PATH
bindir="$(mktemp -d)"
trap 'rm -rf "${bindir}" "${fake}" "${fake}.n"' EXIT
cat >"${bindir}/sleep" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "${bindir}/sleep"
PATH="${bindir}:${PATH}"

if ! bash "${shim}" update --error-on=any >/tmp/flynn-apt-shim.out 2>/tmp/flynn-apt-shim.err; then
  echo "shim should succeed after retries" >&2
  cat /tmp/flynn-apt-shim.err >&2
  exit 1
fi
calls="$(cat "${fake}.n")"
if [[ "${calls}" -ne 3 ]]; then
  echo "expected 3 apt-get calls, got ${calls}" >&2
  exit 1
fi
grep -q 'retrying' /tmp/flynn-apt-shim.err || { echo "shim must log retries" >&2; exit 1; }

# clean / autoclean must not retry
rm -f "${fake}.n"
bash "${shim}" clean >/dev/null 2>&1 || true
calls="$(cat "${fake}.n")"
if [[ "${calls}" -ne 1 ]]; then
  echo "clean must not retry, got ${calls} calls" >&2
  exit 1
fi

# shellcheck source=/dev/null
source "${lib}"
pat="$(flynn_apt_transient_pattern)"
echo 'E: Failed to fetch https://download.docker.com/linux/ubuntu noble InRelease' | grep -qE "${pat}" \
  || { echo "transient pattern must match docker.com fetch failures" >&2; exit 1; }

echo "ok apt-get retry shim + transient pattern"

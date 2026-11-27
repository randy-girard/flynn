#!/bin/bash
# NEW-24: guest/probe-cluster.sh PROBE_SIRENIA must check the sirenia
# membership in the postgres service meta, not only each peer's own status.
# Chaos once reported "recovered" while one peer was listed twice in
# State.Async (it was its own downstream). Stubbed flynn-host, curl and
# hostname; does not boot a VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
guest="${ROOT}/script/vagrant/guest"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin" "${tmp}/fx"

fail() {
  echo "sirenia-membership: $1" >&2
  [[ -f "${tmp}/out" ]] && cat "${tmp}/out" >&2
  exit 1
}

# Stub curl: writes ${STUB_DIR}/<key>.json to -o and prints the code from
# ${STUB_DIR}/<key>.code (default 200). The key is the URL path with / -> _,
# or the peer IP for a /.well-known/status probe.
cat > "${tmp}/bin/curl" <<'SH'
#!/bin/bash
out="" fmt="" url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) fmt="$2"; shift 2 ;;
    -H|--resolve|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
path="/${url#*://*/}"
case "${path}" in
  /.well-known/status) key="peer_$(echo "${url#*://}" | cut -d: -f1)" ;;
  *) key="$(echo "${path%%\?*}" | tr '/.' '__')" ;;
esac
code=200
[[ -f "${STUB_DIR}/${key}.code" ]] && code="$(cat "${STUB_DIR}/${key}.code")"
if [[ -n "${out}" && "${out}" != /dev/null ]]; then
  if [[ -f "${STUB_DIR}/${key}.json" ]]; then cp "${STUB_DIR}/${key}.json" "${out}"; else echo '{}' > "${out}"; fi
fi
[[ -n "${fmt}" ]] && printf '%s' "${code}"
exit 0
SH
cat > "${tmp}/bin/flynn-host" <<'SH'
#!/bin/bash
[[ "$1" == list ]] || exit 1
printf 'ID     ADDR\nhost1  192.168.57.20:1113\nhost2  192.168.57.21:1113\nhost3  192.168.57.22:1113\n'
SH
printf '#!/bin/bash\necho 192.168.57.20\n' > "${tmp}/bin/hostname"
chmod +x "${tmp}/bin/"*
echo '{"env":{"DISCOVERD_AUTH_KEY":"k"}}' > "${tmp}/host.json"

fx="${tmp}/fx"
echo '[{"addr":"10.0.0.9:1"}]' > "${fx}/_services_controller-scheduler_instances.json"
echo '{"data":{"status":"healthy"}}' > "${fx}/_.json"

peer() { # peer <ip> <id>
  printf '{"id":"d-%s","addr":"%s:5432","meta":{"POSTGRES_ID":"%s"}}' "$1" "$1" "$2"
}
A="$(peer 10.0.0.1 id-a)" B="$(peer 10.0.0.2 id-b)" C="$(peer 10.0.0.3 id-c)"
C2="$(peer 10.0.0.4 id-c)" # peer C re-registered at a second address
echo "[${A},${B},${C}]" > "${fx}/_services_postgres_instances.json"

state() { # state <primary> <sync> <async json array> [deposed array]
  printf '{"data":{"generation":7,"primary":%s,"sync":%s,"async":%s,"deposed":%s},"index":3}\n' \
    "$1" "$2" "$3" "${4:-[]}" > "${fx}/_services_postgres_meta.json"
}

probe() {
  STUB_DIR="${fx}" PATH="${tmp}/bin:${PATH}" HOST_JSON="${tmp}/host.json" \
    CLUSTER_DOMAIN=test.local CLUSTER_IP=192.168.57.20 MIN_HOSTS=3 \
    PROBE_QUICK=1 PROBE_SIRENIA=1 bash "${guest}/probe-cluster.sh" > "${tmp}/out" 2>&1
}

expect_ok() {
  probe || fail "$1: probe failed on a healthy membership"
  grep -Fq 'sirenia membership ok' "${tmp}/out" || fail "$1: probe did not report the membership check"
}

expect_fail() { # expect_fail <case> <message>
  if probe; then fail "$1: probe passed"; fi
  grep -Fq "$2" "${tmp}/out" || fail "$1: missing \"$2\""
}

state "${A}" "${B}" "[${C}]"
expect_ok healthy

# A deposed peer that is no longer registered (an old job) is not a member.
state "${A}" "${B}" "[${C}]" "[$(peer 10.0.0.8 id-old)]"
expect_ok "stale deposed"

state "${A}" "${B}" "[${C},${C}]"
expect_fail "async listed twice" "listed twice"

state "${A}" "${B}" "[${C},${C2}]"
expect_fail "async at two addresses" "id-c listed twice"

state "${A}" "${B}" "[${B}]"
expect_fail "sync also async" "id-b listed twice"

state "${A}" "${B}" "[${A}]"
expect_fail "primary is its own downstream" "id-a listed twice"

state "${A}" "${B}" "[]"
expect_fail "registered peer missing from the chain" "id-c is registered but not in the sirenia chain"

state "${A}" "null" "[${C}]"
expect_fail "no sync" "no sync"

echo '{"data":null,"index":0}' > "${fx}/_services_postgres_meta.json"
expect_fail "no state" "no sirenia state"

echo 404 > "${fx}/_services_postgres_meta.code"
expect_fail "meta unavailable" "postgres service meta"
rm -f "${fx}/_services_postgres_meta.code"

# chaos samples health through check-health.sh, which must keep the check on
if ! grep -Fq 'PROBE_SIRENIA=1' "${guest}/check-health.sh"; then
  fail "check-health.sh must run probe-cluster.sh with PROBE_SIRENIA=1 (chaos health)"
fi

echo "sirenia-membership: ok"

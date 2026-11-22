#!/bin/bash
# Behaviour of guest/wait-tenant-http.sh with a stubbed curl. Does not boot a VM.
#
# A pinned app that was deleted while the reload snapshot was taken has no
# route: the router fails the TLS handshake for its hostname (curl 000) and
# answers plain HTTP with 404. The wait must stop covering it instead of
# timing out the reload. An app whose HTTPS fails and whose HTTP is not a 404
# is still down and must keep failing the wait.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
guest="${ROOT}/script/vagrant/guest/wait-tenant-http.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

mkdir -p "${tmp}/bin"
# Stub curl: the last argument is the URL. STUB_<app>_https and
# STUB_<app>_http give the code to print (000 also exits non-zero like a
# failed handshake). Unknown apps answer 200.
cat > "${tmp}/bin/curl" <<'SH'
#!/bin/bash
url="${!#}"
scheme="${url%%://*}"
host="${url#*://}"; host="${host%%/*}"; app="${host%%.*}"; app="${app//-/_}"
var="STUB_${app}_${scheme}"
code="${!var:-200}"
printf '%s' "${code}"
[[ "${code}" == "000" ]] && exit 35
exit 0
SH
chmod +x "${tmp}/bin/curl"
echo '{"env":{}}' > "${tmp}/host.json"

run() {
  PATH="${tmp}/bin:${PATH}" HOST_JSON="${tmp}/host.json" CLUSTER_DOMAIN=test.local \
    SLEEP=0 GONE_TRIES=3 TRIES=8 bash "${guest}" > "${tmp}/out" 2>&1
}

fail() {
  echo "wait-tenant-http: $1" >&2
  cat "${tmp}/out" >&2
  exit 1
}

# Deleted app (TLS refused, HTTP 404): dropped, wait succeeds.
if ! TENANT_APPS="gone-app" STUB_gone_app_https=000 STUB_gone_app_http=404 run; then
  fail "a pinned app with no route must not fail the wait"
fi
grep -Fq "gone-app has no route" "${tmp}/out" || fail "must say the app has no route"

# Healthy app plus a deleted app: success, healthy app still checked.
if ! TENANT_APPS="live-app gone-app" STUB_gone_app_https=000 STUB_gone_app_http=404 run; then
  fail "healthy plus deleted app must pass"
fi
grep -Fq "live-app=200" "${tmp}/out" || fail "healthy app must still be reported"

# Down app (TLS fails, HTTP also fails): the wait still fails.
if TENANT_APPS="down-app" STUB_down_app_https=000 STUB_down_app_http=000 run; then
  fail "an app that answers neither HTTPS nor HTTP must fail the wait"
fi
grep -Fq "tenant HTTP still down: down-app=000" "${tmp}/out" || fail "must report the down app"

# No backends (HTTPS 503): still down even though HTTP would be 404-free.
if TENANT_APPS="busy-app" STUB_busy_app_https=503 STUB_busy_app_http=404 run; then
  fail "an app answering 503 over HTTPS must fail the wait"
fi

# A missing route for fewer than GONE_TRIES rounds is not enough: an app that
# is gone for two rounds then answers stays covered and passes once healthy.
state="${tmp}/flap"
cat > "${tmp}/bin/curl" <<SH
#!/bin/bash
url="\${!#}"
n=\$(cat "${state}" 2>/dev/null || echo 0)
if [[ "\${url}" == https://* ]]; then
  n=\$((n + 1)); echo "\${n}" > "${state}"
  if [[ \${n} -le 2 ]]; then printf 000; exit 35; fi
  printf 200; exit 0
fi
printf 404
SH
if ! TENANT_APPS="flap-app" run; then
  fail "an app that returns after a short TLS outage must pass"
fi
if grep -Fq "has no route" "${tmp}/out"; then
  fail "an app missing for fewer than GONE_TRIES rounds must not be dropped"
fi
grep -Fq "tenant HTTP ok: flap-app=200" "${tmp}/out" || fail "flapping app must end ok"

echo "wait-tenant-http ok"

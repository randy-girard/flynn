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

# A single health sample (TRIES=1, the chaos and monitor tenants section)
# starts the GONE_TRIES count at zero every time, so a deleted app that a
# host still lists stayed "down" for the whole 300 s chaos baseline
# (EDGE-99). With ROUTER_IPS (every running node), no route on every router
# is enough in one round. A 404 from only some routers is not.
cat > "${tmp}/bin/curl" <<'SH'
#!/bin/bash
url="${!#}" resolve=""
while [[ $# -gt 0 ]]; do [[ "$1" == --resolve ]] && resolve="$2"; shift; done
ip="${resolve##*:}"
if [[ "${url}" == https://* ]]; then printf 000; exit 35; fi
var="STUB_HTTP_${ip//./_}"
printf '%s' "${!var:-404}"
SH
if ! TENANT_APPS="gone-app" ROUTER_IPS="10.0.0.1 10.0.0.2 10.0.0.3" TRIES=1 GONE_TRIES=6 \
  PATH="${tmp}/bin:${PATH}" HOST_JSON="${tmp}/host.json" CLUSTER_DOMAIN=test.local SLEEP=0 bash "${guest}" > "${tmp}/out" 2>&1; then
  fail "one sample must drop an app that has no route on every router"
fi
grep -Fq "gone-app has no route on 3 routers (deleted?); not waiting for it" "${tmp}/out" || fail "must say the app has no route on every router"
if TENANT_APPS="gone-app" ROUTER_IPS="10.0.0.1 10.0.0.2 10.0.0.3" STUB_HTTP_10_0_0_2=000 TRIES=1 GONE_TRIES=6 \
  PATH="${tmp}/bin:${PATH}" HOST_JSON="${tmp}/host.json" CLUSTER_DOMAIN=test.local SLEEP=0 bash "${guest}" > "${tmp}/out" 2>&1; then
  fail "an app with a route left on one router must still fail a single sample"
fi
grep -Fq "tenant HTTP still down: gone-app=000" "${tmp}/out" || fail "must report the app as down"
# Without ROUTER_IPS one sample is not enough (unchanged).
if TENANT_APPS="gone-app" TRIES=1 GONE_TRIES=6 \
  PATH="${tmp}/bin:${PATH}" HOST_JSON="${tmp}/host.json" CLUSTER_DOMAIN=test.local SLEEP=0 bash "${guest}" > "${tmp}/out" 2>&1; then
  fail "without ROUTER_IPS a single sample must not drop the app"
fi

# LIST_ONLY=1 is the pre-reload snapshot. It must fail, not print an empty
# list, when discoverd or a host cannot be read: rolling reload would otherwise
# take the list again after the crash and miss apps whose jobs died with it.
# A small fake serves discoverd's flynn-host instances and each host's jobs.
port_file="${tmp}/port"
cat > "${tmp}/fake.py" <<'FAKE'
import http.server, json, sys
mode = sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        port = self.server.server_address[1]
        if self.path.startswith("/services/flynn-host/instances"):
            if mode == "discoverd-down":
                return self.send_error(503)
            hosts = [] if mode == "no-hosts" else [
                {"addr": "127.0.0.1:%d" % port},
                {"addr": "127.0.0.1:%d" % (1 if mode == "host-down" else port)},
            ]
            body = json.dumps(hosts).encode()
        elif self.path.startswith("/host/jobs"):
            body = json.dumps({
                "a": {"job": {"metadata": {"flynn-controller.app_name": "app-one"}}},
                "b": {"job": {"metadata": {"flynn-controller.app_name": "router", "flynn-system-app": "true"}}},
            }).encode()
        else:
            return self.send_error(404)
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)
s = http.server.HTTPServer(("127.0.0.1", 0), H)
open(sys.argv[1], "w").write(str(s.server_address[1]))
s.serve_forever()
FAKE
list_with() {
  local mode="$1" pid st=0
  rm -f "${port_file}"
  python3 "${tmp}/fake.py" "${port_file}" "${mode}" &
  pid=$!
  for _ in $(seq 1 50); do [[ -s "${port_file}" ]] && break; sleep 0.1; done
  DISCOVERD_URL="http://127.0.0.1:$(cat "${port_file}")" LIST_ONLY=1 TENANT_APPS="" run || st=$?
  kill "${pid}" 2>/dev/null || true
  wait "${pid}" 2>/dev/null || true
  return "${st}"
}
list_with ok || fail "LIST_ONLY must succeed when discoverd and every host answer"
[[ "$(cat "${tmp}/out")" == "app-one" ]] || fail "LIST_ONLY must print only tenant apps"
if list_with discoverd-down; then
  fail "LIST_ONLY must fail when discoverd cannot be read"
fi
grep -Fq "cannot list tenant apps" "${tmp}/out" || fail "LIST_ONLY must say why the list failed"
if list_with host-down; then
  fail "LIST_ONLY must fail when a host's jobs cannot be read"
fi
if list_with no-hosts; then
  fail "LIST_ONLY must fail when discoverd lists no hosts"
fi

echo "wait-tenant-http ok"

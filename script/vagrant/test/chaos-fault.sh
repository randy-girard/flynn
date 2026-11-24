#!/bin/bash
# Behaviour of guest/chaos-fault.sh against a stub discoverd, controller and
# flynn-host API, with a stubbed flynn-host binary. Does not boot a VM.
#
# The pools are only right when the script knows which apps are datastores
# (from the controller's provider resources) and which job of each is the
# leader (from discoverd). When either lookup fails it must not guess: a
# controller error fails the run without stopping a job, and a datastore
# whose leader is unknown is left out of both datastore pools. A replica is a
# database peer (an instance of the datastore's discoverd service) that is not
# the leader, never another process of the same app such as the postgres
# `web` API job; when the instances cannot be listed the datastore is left out
# of both pools too.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
guest="${ROOT}/script/vagrant/guest/chaos-fault.sh"
tmp="$(mktemp -d)"
server_pid=""
cleanup() {
  [[ -n "${server_pid}" ]] && kill "${server_pid}" 2>/dev/null || true
  rm -rf "${tmp}"
}
trap cleanup EXIT

mkdir -p "${tmp}/bin"
# Stub flynn-host: records each stop.
cat > "${tmp}/bin/flynn-host" <<'SH'
#!/bin/bash
echo "$*" >> "${STOP_LOG:?}"
SH
chmod +x "${tmp}/bin/flynn-host"
echo '{"env":{}}' > "${tmp}/host.json"

# Stub HTTP server. routes.json maps "path?query" to [status, body]; @ADDR@
# in a body becomes the server's own host:port, so controller and host
# instances point back at it. Unknown paths answer 404.
cat > "${tmp}/server.py" <<'PY'
import http.server, json, sys
routes_file, port_file = sys.argv[1], sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        routes = json.load(open(routes_file))
        status, body = routes.get(self.path, [404, {"error": "not found"}])
        data = json.dumps(body).replace("@ADDR@", "127.0.0.1:%d" % self.server.server_port).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def log_message(self, *a):
        pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
open(port_file, "w").write(str(s.server_port))
s.serve_forever()
PY
echo '{}' > "${tmp}/routes.json"
python3 "${tmp}/server.py" "${tmp}/routes.json" "${tmp}/port" &
server_pid=$!
for _ in $(seq 50); do [[ -s "${tmp}/port" ]] && break; sleep 0.1; done
[[ -s "${tmp}/port" ]] || { echo "chaos-fault: stub server did not start" >&2; exit 1; }
addr="127.0.0.1:$(cat "${tmp}/port")"

# routes writes the stub API. Flags change one answer each:
#   ctl_instances=500  discoverd cannot list controller instances
#   providers=500      every controller instance fails GET /providers
#   first_ctl_down=1   the first controller instance fails, the second works
#   pg_leader=500      discoverd fails the postgres leader lookup
#   pg_instances=500   discoverd fails the postgres instances lookup
#   mysql=1            add a mysql resource (leader 10.0.0.4, replica 10.0.0.5)
routes() {
  local ctl_instances=200 providers=200 first_ctl_down=0 pg_leader=200 pg_instances=200 mysql=0 kv
  for kv in "$@"; do local "${kv}"; done
  python3 - "${tmp}/routes.json" "${ctl_instances}" "${providers}" "${first_ctl_down}" "${pg_leader}" "${pg_instances}" "${mysql}" <<'PY'
import json, sys
out, ctl_instances, providers, first_ctl_down, pg_leader, pg_instances, mysql = sys.argv[1:]
ctl = [{"addr": "@ADDR@"}]
r = {}
if first_ctl_down == "1":
    # A dead first instance answers on a closed port.
    ctl = [{"addr": "127.0.0.1:1"}] + ctl
r["/services/controller/instances"] = [int(ctl_instances), ctl]
res = [{"env": {"DATABASE_URL": "postgres://u:p@leader.postgres.discoverd:5432/db"}}]
if mysql == "1":
    res.append({"env": {"MYSQL_URL": "mysql://u:p@leader.mysql.discoverd:3306/db"}})
r["/providers"] = [int(providers), [{"id": "p1"}]]
r["/providers/p1/resources"] = [200, res]
r["/services/postgres/leader"] = [int(pg_leader), {"addr": "10.0.0.1:5432"}]
r["/services/mysql/leader"] = [200, {"addr": "10.0.0.4:3306"}]
# The database peers only; the postgres web job (10.0.0.30) registers as
# postgres-api, not postgres.
r["/services/postgres/instances"] = [int(pg_instances), [{"addr": "10.0.0.%d:5432" % i} for i in (1, 2, 3)]]
r["/services/mysql/instances"] = [200, [{"addr": "10.0.0.4:3306"}, {"addr": "10.0.0.5:3306"}]]
r["/services/flynn-host/instances"] = [200, [{"addr": "@ADDR@", "meta": {"id": "host1"}}]]
def job(i, app, ip, system=True, typ="web"):
    md = {"flynn-controller.app_name": app, "flynn-controller.type": typ}
    if system:
        md["flynn-system-app"] = "true"
    return {"status": 1, "host_id": "host1", "internal_ip": ip, "job": {"id": i, "metadata": md}}
jobs = [
    job("job-pg-leader", "postgres", "10.0.0.1", typ="postgres"),
    job("job-pg-replica1", "postgres", "10.0.0.2", typ="postgres"),
    job("job-pg-replica2", "postgres", "10.0.0.3", typ="postgres"),
    job("job-pg-web", "postgres", "10.0.0.30", typ="web"),
    job("job-router", "router", "10.0.0.10", typ="app"),
    job("job-tenant", "tenant-app", "10.0.0.20", system=False),
]
if mysql == "1":
    jobs += [job("job-my-leader", "mysql", "10.0.0.4", typ="mysql"),
             job("job-my-replica", "mysql", "10.0.0.5", typ="mysql")]
r["/host/jobs?active=true"] = [200, {j["job"]["id"]: j for j in jobs}]
json.dump(r, open(out, "w"))
PY
}

st=0
run() {
  local fault="$1" seed="$2"
  : > "${tmp}/stops"
  st=0
  PATH="${tmp}/bin:${PATH}" STOP_LOG="${tmp}/stops" HOST_JSON="${tmp}/host.json" \
    DISCOVERD_URL="http://${addr}" FAULT="${fault}" SEED="${seed}" \
    bash "${guest}" > "${tmp}/out" 2>&1 || st=$?
}

fail() {
  echo "chaos-fault: $1" >&2
  cat "${tmp}/out" >&2
  exit 1
}

stopped() { sed -n 's/^stop //p' "${tmp}/stops"; }

expect_error() {
  local what="$1"
  [[ "${st}" -ne 0 ]] || fail "${what}: must exit non-zero"
  [[ "${st}" -ne 3 ]] || fail "${what}: must fail, not report nothing to hit (exit 3)"
  [[ ! -s "${tmp}/stops" ]] || fail "${what}: must not stop a job (stopped $(stopped))"
  ! grep -q '^target ' "${tmp}/out" || fail "${what}: must not print a target"
}

# Healthy stubs: every pool is what the fault name says.
routes
for seed in $(seq 1 16); do
  run system-job "${seed}"
  [[ "${st}" -eq 0 ]] || fail "system-job must succeed (exit ${st})"
  [[ "$(stopped)" == "job-router" ]] || fail "system-job must stop only non-datastore system jobs, stopped $(stopped)"
  run datastore-primary "${seed}"
  [[ "$(stopped)" == "job-pg-leader" ]] || fail "datastore-primary must stop the leader, stopped $(stopped)"
  run datastore-replica "${seed}"
  case "$(stopped)" in
    job-pg-replica1|job-pg-replica2) ;;
    *) fail "datastore-replica must stop a non-leader database peer (not the web API job), stopped $(stopped)" ;;
  esac
  run tenant-job "${seed}"
  [[ "$(stopped)" == "job-tenant" ]] || fail "tenant-job must stop the tenant job, stopped $(stopped)"
done
grep -Fq "target kind=tenant-job app=tenant-app" "${tmp}/out" || fail "must print the target"

# Controller errors: no way to tell datastores apart, so no job is stopped.
routes providers=500
for fault in system-job tenant-job datastore-primary datastore-replica; do
  run "${fault}" 1
  expect_error "${fault} with every controller failing /providers"
  grep -Fq "controller" "${tmp}/out" || fail "${fault}: must say the controller lookup failed"
done
routes ctl_instances=500
run system-job 1
expect_error "system-job when discoverd cannot list controller instances"

# plugin-job does not need the datastore list.
routes providers=500
run plugin-job 1
[[ "${st}" -eq 3 ]] || fail "plugin-job must not depend on the controller (exit ${st})"

# One controller instance down, the next answers: still a normal pick.
routes first_ctl_down=1
for seed in 1 2 3 4; do
  run system-job "${seed}"
  [[ "${st}" -eq 0 && "$(stopped)" == "job-router" ]] || fail "system-job must fall back to the next controller instance, stopped $(stopped) exit ${st}"
done

# Unknown postgres leader, postgres the only datastore: neither datastore
# pool may contain postgres, and with nothing else to pick the run fails.
routes pg_leader=500
for fault in datastore-primary datastore-replica; do
  run "${fault}" 1
  expect_error "${fault} with the postgres leader unknown"
  grep -Fq "postgres" "${tmp}/out" || fail "${fault}: must name the datastore whose leader is unknown"
done
# The unknown leader does not affect the other kinds.
run system-job 1
[[ "${st}" -eq 0 && "$(stopped)" == "job-router" ]] || fail "system-job must not depend on leader lookups, stopped $(stopped)"

# Postgres instances cannot be listed: the replicas cannot be told apart from
# the web job, so postgres is left out of both pools.
routes pg_instances=500
for fault in datastore-primary datastore-replica; do
  run "${fault}" 1
  expect_error "${fault} with the postgres instances unknown"
  grep -Fq "postgres" "${tmp}/out" || fail "${fault}: must name the datastore whose instances are unknown"
done
routes pg_instances=500 mysql=1
for seed in 1 2 3 4; do
  run datastore-replica "${seed}"
  [[ "${st}" -eq 0 && "$(stopped)" == "job-my-replica" ]] || fail "datastore-replica must skip postgres when its instances are unknown, stopped $(stopped) exit ${st}"
done

# Unknown postgres leader, mysql known: only mysql jobs are picked.
routes pg_leader=500 mysql=1
for seed in 1 2 3 4 5 6 7 8; do
  run datastore-primary "${seed}"
  [[ "${st}" -eq 0 && "$(stopped)" == "job-my-leader" ]] || fail "datastore-primary must skip postgres and stop the mysql leader, stopped $(stopped) exit ${st}"
  run datastore-replica "${seed}"
  [[ "${st}" -eq 0 && "$(stopped)" == "job-my-replica" ]] || fail "datastore-replica must skip postgres and stop the mysql replica, stopped $(stopped) exit ${st}"
done
grep -Fq "leader of postgres unknown" "${tmp}/out" || fail "must report the excluded datastore"

echo "chaos-fault: ok"

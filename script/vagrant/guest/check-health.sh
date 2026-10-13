#!/bin/bash
# Run on a cluster node as root. One health sample for script/vagrant.sh
# monitor and chaos: cluster probe (hosts, controller, scheduler, postgres
# peers, every service at its formation count, status), tenant HTTP, plugin
# APIs, the dashboard, and every provider resource's connection endpoint.
# Prints "ok <section>" or "FAIL <section>: <reason>" per section and exits 1
# if any section failed. Never prints credentials or resource env values.
set -uo pipefail
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
IP="${CLUSTER_IP:-}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export FLYNN_SKIP_UPDATE_CHECK=1
failed=()

section() {
  local name="$1" out st
  shift
  out="$("$@" 2>&1)"
  st=$?
  if [[ "${st}" -eq 0 ]]; then
    echo "ok ${name}: $(printf '%s\n' "${out}" | grep -v '^[[:space:]]*$' | tail -1)"
  else
    echo "FAIL ${name}: $(printf '%s\n' "${out}" | grep -v '^[[:space:]]*$' | tail -3 | paste -sd ';' -)"
    failed+=("${name}")
  fi
}

section cluster env PROBE_QUICK=1 PROBE_SIRENIA=1 PROBE_FULL_STRENGTH=1 \
  bash "${here}/probe-cluster.sh"
section tenants env TRIES=1 bash "${here}/wait-tenant-http.sh"
section plugins python3 - "${DOMAIN}" "${IP}" <<'PY'
import base64, json, socket, subprocess, sys, urllib.error, urllib.parse, urllib.request

domain, cluster_ip = sys.argv[1], sys.argv[2]
env = json.load(open("/etc/flynn/host.json")).get("env") or {}
D = {"Auth-Key": env.get("DISCOVERD_AUTH_KEY") or ""}
C = {"Authorization": "Basic " + base64.b64encode((":" + (env.get("CONTROLLER_KEY") or "")).encode()).decode(),
     "Accept": "application/json"}
PORTS = {"mysql": 3306, "postgres": 5432, "postgresql": 5432, "redis": 6379}


def get(url, headers=None, timeout=5):
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, b""
    except Exception:
        return 0, b""


def get_json(url, headers):
    code, body = get(url, headers)
    if code != 200:
        raise RuntimeError("HTTP %s" % (code or "none"))
    return json.loads(body.decode() or "null")


def instances(svc):
    try:
        return get_json("http://127.0.0.1:1111/services/%s/instances" % svc, D) or []
    except Exception:
        return []


ctl = None
for inst in instances("controller"):
    try:
        get_json("http://%s/providers" % inst["addr"], C)
        ctl = inst["addr"]
        break
    except Exception:
        continue
if not ctl:
    raise SystemExit("controller API unavailable")

bad = []
plugin_svcs = set()
for f in get_json("http://%s/formations?active=true" % ctl, C) or []:
    app = (f.get("app") or {}).get("name") or f.get("app_name") or ""
    if not app.endswith("-plugin"):
        continue
    procs = (f.get("release") or {}).get("processes") or {}
    for name, n in (f.get("processes") or {}).items():
        p = procs.get(name) or {}
        if n <= 0:
            continue
        for pt in p.get("ports") or []:
            s = (pt.get("service") or {}).get("name")
            if s:
                plugin_svcs.add(s)
for svc in sorted(plugin_svcs):
    insts = instances(svc)
    if not insts:
        bad.append("%s has no instances" % svc)
    for inst in insts:
        code = 0
        for path in ("/.well-known/status", "/ping", "/"):
            code, _ = get("http://%s%s" % (inst["addr"], path), timeout=4)
            if 200 <= code < 300:
                break
        if not 0 < code < 500:
            bad.append("%s@%s HTTP %s" % (svc, inst["addr"], code or "none"))

dash = "-"
if "dashboard" in plugin_svcs:
    cmd = ["curl", "-sk", "--max-time", "5", "-o", "/dev/null", "-w", "%{http_code}"]
    if cluster_ip:
        cmd += ["--resolve", "dashboard.%s:443:%s" % (domain, cluster_ip)]
    cmd.append("https://dashboard.%s/.well-known/status" % domain)
    try:
        dash = subprocess.run(cmd, capture_output=True, timeout=8).stdout.decode().strip() or "000"
    except Exception:
        dash = "000"
    if dash != "200":
        bad.append("dashboard.%s HTTP %s" % (domain, dash))

endpoints = {}
for prov in get_json("http://%s/providers" % ctl, C) or []:
    for res in get_json("http://%s/providers/%s/resources" % (ctl, prov["id"]), C) or []:
        for key, val in sorted((res.get("env") or {}).items()):
            if not key.endswith("_URL") or "://" not in (val or ""):
                continue
            u = urllib.parse.urlsplit(val)
            host = u.hostname or ""
            if not host.endswith(".discoverd"):
                continue
            port = u.port or PORTS.get(u.scheme, 0)
            endpoints[(host[:-len(".discoverd")], port)] = prov.get("name") or prov["id"]
for (name, port), prov in sorted(endpoints.items()):
    svc = name[len("leader."):] if name.startswith("leader.") else name
    if name.startswith("leader."):
        try:
            addrs = [get_json("http://127.0.0.1:1111/services/%s/leader" % svc, D)["addr"]]
        except Exception:
            bad.append("%s/%s has no leader" % (prov, svc))
            continue
    else:
        addrs = [i["addr"] for i in instances(svc)]
        if not addrs:
            bad.append("%s/%s has no instances" % (prov, svc))
            continue
    for addr in addrs:
        ip = addr.rsplit(":", 1)[0]
        try:
            socket.create_connection((ip, port or int(addr.rsplit(":", 1)[1])), timeout=4).close()
        except Exception as e:
            bad.append("%s/%s %s:%s %s" % (prov, svc, ip, port, type(e).__name__))
if bad:
    raise SystemExit("; ".join(bad))
print("plugin services=%d dashboard=%s resource endpoints=%d" % (len(plugin_svcs), dash, len(endpoints)))
PY

# Resource accounting: every provider resource is listed with its app's
# formation size, printed as inventory=id:app:nodes,... on the ok line.
# EXPECT_RESOURCES (a list in that format, taken before chaos) must still be
# present at the same size. mongodb, clickhouse and kafka resources must also
# have one running job per node, each reporting service_available=1 and full
# cluster membership on its /metrics/log (FLYNN_METRICS_PORT 9180).
section resources python3 - "${EXPECT_RESOURCES:-}" <<'PY'
import base64, json, re, sys, urllib.error, urllib.parse, urllib.request

expect = [e for e in sys.argv[1].split(",") if e]
env = json.load(open("/etc/flynn/host.json")).get("env") or {}
D = {"Auth-Key": env.get("DISCOVERD_AUTH_KEY") or ""}
C = {"Authorization": "Basic " + base64.b64encode((":" + (env.get("CONTROLLER_KEY") or "")).encode()).decode(),
     "Accept": "application/json"}
CLUSTER = ("mongodb", "clickhouse", "kafka")


def get(url, headers=None, timeout=5):
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers or {}), timeout=timeout) as r:
        return r.read().decode()


def get_json(url, headers):
    return json.loads(get(url, headers) or "null")


def instances(svc):
    try:
        return get_json("http://127.0.0.1:1111/services/%s/instances" % svc, D) or []
    except Exception:
        return []


ctl = None
for inst in instances("controller"):
    try:
        get_json("http://%s/providers" % inst["addr"], C)
        ctl = inst["addr"]
        break
    except Exception:
        continue
if not ctl:
    raise SystemExit("controller API unavailable")

nodes = {}
for f in get_json("http://%s/formations?active=true" % ctl, C) or []:
    app = (f.get("app") or {}).get("name") or f.get("app_name") or ""
    nodes[app] = sum(n for n in (f.get("processes") or {}).values())

found = {}  # resource id -> (provider, app, nodes)
for prov in get_json("http://%s/providers" % ctl, C) or []:
    for res in get_json("http://%s/providers/%s/resources" % (ctl, prov["id"]), C) or []:
        app = ""
        for key, val in sorted((res.get("env") or {}).items()):
            host = urllib.parse.urlsplit(val).hostname or "" if key.endswith("_URL") and "://" in (val or "") else ""
            if host.endswith(".discoverd"):
                app = host[:-len(".discoverd")].removeprefix("leader.")
                break
        found[res["id"]] = (prov.get("name") or prov["id"], app, nodes.get(app, 0))

bad = []
for e in expect:
    rid, app, n = (e.split(":") + ["", ""])[:3]
    if rid not in found:
        bad.append("resource %s (%s) is missing" % (rid, app))
    elif found[rid][1] != app or found[rid][2] != int(n or 0):
        bad.append("resource %s (%s) has %d nodes, expected %s" % (rid, app, found[rid][2], n))

want = {
    "mongodb": {"members", "healthy_members"},
    "kafka": {"brokers", "live_brokers", "under_replicated_partitions", "offline_partitions"},
    "clickhouse": {"readonly_replicas"},
}
checked = 0
for rid, (provider, app, n) in sorted(found.items()):
    if provider not in CLUSTER or not app:
        continue
    checked += 1
    insts = instances(app)
    if len(insts) != n:
        bad.append("%s %s has %d running jobs, formation has %d" % (provider, app, len(insts), n))
    for inst in insts:
        ip = inst["addr"].rsplit(":", 1)[0]
        try:
            line = get("http://%s:9180/metrics/log" % ip, timeout=6)
        except Exception as ex:
            bad.append("%s %s@%s metrics: %s" % (provider, app, ip, type(ex).__name__))
            continue
        s = {k: float(v) for k, v in re.findall(r"sample#(\w+)=(-?[0-9.]+)", line)}
        if s.get("service_available") != 1:
            bad.append("%s %s@%s service_available=%s" % (provider, app, ip, s.get("service_available")))
        elif provider == "mongodb" and not s.get("members") == s.get("healthy_members") == n:
            bad.append("%s %s@%s members=%s healthy=%s want %d" % (provider, app, ip, s.get("members"), s.get("healthy_members"), n))
        elif provider == "kafka" and not (s.get("brokers") == s.get("live_brokers") == n
                                          and s.get("under_replicated_partitions", 0) == 0
                                          and s.get("offline_partitions", 0) == 0):
            bad.append("%s %s@%s brokers=%s live=%s urp=%s offline=%s want %d" % (
                provider, app, ip, s.get("brokers"), s.get("live_brokers"),
                s.get("under_replicated_partitions"), s.get("offline_partitions"), n))
        elif provider == "clickhouse" and s.get("readonly_replicas", 0) != 0:
            bad.append("%s %s@%s readonly_replicas=%s" % (provider, app, ip, s.get("readonly_replicas")))
if bad:
    raise SystemExit("; ".join(bad))
print("resources=%d expected=%d cluster-engines=%d inventory=%s" % (
    len(found), len(expect), checked,
    ",".join("%s:%s:%d" % (rid, v[1], v[2]) for rid, v in sorted(found.items()) if v[1])))
PY

if [[ ${#failed[@]} -gt 0 ]]; then
  echo "unhealthy: ${failed[*]}"
  exit 1
fi
echo "healthy"

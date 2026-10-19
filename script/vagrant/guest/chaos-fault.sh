#!/bin/bash
# Run on a cluster node as root. Stop one random running job of the kind in
# FAULT (no drain, like a crash) for script/vagrant.sh chaos:
#   system-job         Layer 1 job (controller, router, discoverd, flannel, ...)
#   plugin-job         a *-plugin app job (dashboard, datastore APIs, ...)
#   datastore-primary  the leader job of a provider resource (forces failover)
#   datastore-replica  a non-leader job of a provider resource
#   tenant-job         a job of a user app
# APP_MATCH (optional) limits the datastore kinds to apps whose name contains
# it, e.g. kafka. SEED makes the pick repeatable. Prints "target ..." and exits 3 when no job
# of that kind is running.
set -euo pipefail
export FLYNN_SKIP_UPDATE_CHECK=1

python3 - "${FAULT:?FAULT is required}" "${SEED:-}" "${APP_MATCH:-}" <<'PY'
import base64, json, random, subprocess, sys, time, urllib.parse, urllib.request

kind, seed, app_match = sys.argv[1], sys.argv[2], sys.argv[3]
if seed:
    random.seed(seed)
env = json.load(open("/etc/flynn/host.json")).get("env") or {}
D = {"Auth-Key": env.get("DISCOVERD_AUTH_KEY") or ""}
H = {"Auth-Key": env.get("FLYNN_HOST_AUTH_KEY") or ""}
C = {"Authorization": "Basic " + base64.b64encode((":" + (env.get("CONTROLLER_KEY") or "")).encode()).decode(),
     "Accept": "application/json"}


def get(url, headers):
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=8) as r:
        return json.load(r)


def leader_ip(svc):
    try:
        return get("http://127.0.0.1:1111/services/%s/leader" % svc, D)["addr"].rsplit(":", 1)[0]
    except Exception:
        return None


# Datastore apps are the discoverd services behind provider resource URLs.
datastores = set()
for inst in get("http://127.0.0.1:1111/services/controller/instances", D) or []:
    try:
        for prov in get("http://%s/providers" % inst["addr"], C) or []:
            for res in get("http://%s/providers/%s/resources" % (inst["addr"], prov["id"]), C) or []:
                for key, val in (res.get("env") or {}).items():
                    if key.endswith("_URL") and "://" in (val or ""):
                        host = urllib.parse.urlsplit(val).hostname or ""
                        # leader.<app>.discoverd (mysql, postgres, redis,
                        # mongodb) or <app>.discoverd (clickhouse, kafka).
                        if host.endswith(".discoverd"):
                            datastores.add(host[:-len(".discoverd")].removeprefix("leader."))
        break
    except Exception:
        continue

jobs = []
for h in get("http://127.0.0.1:1111/services/flynn-host/instances", D) or []:
    try:
        data = get("http://%s/host/jobs?active=true" % h["addr"], H)
    except Exception:
        continue
    for a in (data.values() if isinstance(data, dict) else data) or []:
        if a.get("status") != 1:
            continue
        j = a.get("job") or {}
        md = j.get("metadata") or {}
        jobs.append({
            "id": j.get("id"), "host": a.get("host_id") or (h.get("meta") or {}).get("id"),
            "ip": a.get("internal_ip"), "app": md.get("flynn-controller.app_name") or "",
            "type": md.get("flynn-controller.type") or "",
            "system": (md.get("flynn-system-app") or "").lower() == "true",
        })

leaders = {s: leader_ip(s) for s in datastores}
if kind == "system-job":
    pool = [j for j in jobs if j["system"] and j["app"] not in datastores and not j["app"].endswith("-plugin")]
elif kind == "plugin-job":
    pool = [j for j in jobs if j["app"].endswith("-plugin")]
elif kind == "datastore-primary":
    pool = [j for j in jobs if j["app"] in datastores and j["ip"] and j["ip"] == leaders.get(j["app"])]
elif kind == "datastore-replica":
    pool = [j for j in jobs if j["app"] in datastores and j["ip"] != leaders.get(j["app"])]
elif kind == "tenant-job":
    pool = [j for j in jobs if not j["system"] and j["app"] and j["app"] not in datastores
            and not j["app"].endswith("-plugin")]
else:
    raise SystemExit("unknown FAULT %s" % kind)
pool = [j for j in pool if j["id"]]
if app_match and kind.startswith("datastore-"):
    pool = [j for j in pool if app_match in j["app"]]
if not pool:
    print("target kind=%s none running" % kind)
    raise SystemExit(3)
t = random.choice(sorted(pool, key=lambda j: j["id"]))
print("target kind=%s app=%s type=%s host=%s job=%s" % (kind, t["app"], t["type"], t["host"], t["id"]))
sys.stdout.flush()
# Stop can sit behind the host state lock, and the cluster client gives up
# after 10s of waiting for headers. Retry, and treat "already stopped" as
# success when the first request stopped the job but the client timed out.
rc = 1
for _ in range(3):
    stop = subprocess.run(["flynn-host", "stop", t["id"]], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    text = stop.stdout.strip()
    if stop.returncode == 0 or "already stopped" in text:
        rc = 0
        break
    print(stop.stdout.strip())
    rc = stop.returncode
    time.sleep(2)
if rc != 0:
    raise SystemExit(rc)
PY

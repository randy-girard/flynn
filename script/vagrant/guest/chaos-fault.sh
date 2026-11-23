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
#
# It does not guess when a lookup fails. If no controller instance lists the
# provider resources, datastore apps cannot be told apart from other system
# or tenant jobs, so it exits 2 without stopping anything (plugin-job does
# not need that list). A datastore whose discoverd leader lookup fails is
# left out of both datastore pools; if that leaves the pool empty it exits 2
# instead of 3. DISCOVERD_URL and HOST_JSON override the local discoverd and
# /etc/flynn/host.json (tests).
set -euo pipefail
export FLYNN_SKIP_UPDATE_CHECK=1

python3 - "${FAULT:?FAULT is required}" "${SEED:-}" "${APP_MATCH:-}" <<'PY'
import base64, json, os, random, subprocess, sys, time, urllib.parse, urllib.request

kind, seed, app_match = sys.argv[1], sys.argv[2], sys.argv[3]
if seed:
    random.seed(seed)
DISCOVERD = os.environ.get("DISCOVERD_URL") or "http://127.0.0.1:1111"
env = json.load(open(os.environ.get("HOST_JSON") or "/etc/flynn/host.json")).get("env") or {}
D = {"Auth-Key": env.get("DISCOVERD_AUTH_KEY") or ""}
H = {"Auth-Key": env.get("FLYNN_HOST_AUTH_KEY") or ""}
C = {"Authorization": "Basic " + base64.b64encode((":" + (env.get("CONTROLLER_KEY") or "")).encode()).decode(),
     "Accept": "application/json"}


def get(url, headers):
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=8) as r:
        return json.load(r)


def leader_ip(svc):
    """The leader's IP, or None with the reason when discoverd cannot say."""
    try:
        return get(DISCOVERD + "/services/%s/leader" % svc, D)["addr"].rsplit(":", 1)[0], None
    except Exception as e:
        return None, "%s: %s" % (type(e).__name__, e)


def list_datastores():
    """Datastore apps are the discoverd services behind provider resource
    URLs. Asks each controller instance in turn and returns the first full
    answer; exits 2 when none gives one."""
    errors = []
    try:
        instances = get(DISCOVERD + "/services/controller/instances", D) or []
    except Exception as e:
        instances = []
        errors.append("discoverd controller instances: %s: %s" % (type(e).__name__, e))
    for inst in instances:
        found = set()
        try:
            for prov in get("http://%s/providers" % inst["addr"], C) or []:
                for res in get("http://%s/providers/%s/resources" % (inst["addr"], prov["id"]), C) or []:
                    for key, val in (res.get("env") or {}).items():
                        if key.endswith("_URL") and "://" in (val or ""):
                            host = urllib.parse.urlsplit(val).hostname or ""
                            # leader.<app>.discoverd (mysql, postgres, redis,
                            # mongodb) or <app>.discoverd (clickhouse, kafka).
                            if host.endswith(".discoverd"):
                                found.add(host[:-len(".discoverd")].removeprefix("leader."))
            return found
        except Exception as e:
            errors.append("controller %s: %s: %s" % (inst.get("addr"), type(e).__name__, e))
    if not errors:
        errors.append("discoverd lists no controller instances")
    print("error: cannot list provider resources from the controller, so datastore jobs cannot be told apart; not stopping a job")
    for e in errors:
        print("  " + e)
    raise SystemExit(2)


datastores = set() if kind == "plugin-job" else list_datastores()

jobs = []
for h in get(DISCOVERD + "/services/flynn-host/instances", D) or []:
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

leaders, unknown = {}, {}
if kind.startswith("datastore-"):
    for svc in sorted(datastores):
        ip, err = leader_ip(svc)
        if ip:
            leaders[svc] = ip
        else:
            unknown[svc] = err
            print("note: leader of %s unknown (%s); left out of the datastore pools" % (svc, err))
if kind == "system-job":
    pool = [j for j in jobs if j["system"] and j["app"] not in datastores and not j["app"].endswith("-plugin")]
elif kind == "plugin-job":
    pool = [j for j in jobs if j["app"].endswith("-plugin")]
elif kind == "datastore-primary":
    pool = [j for j in jobs if j["app"] in leaders and j["ip"] and j["ip"] == leaders[j["app"]]]
elif kind == "datastore-replica":
    pool = [j for j in jobs if j["app"] in leaders and j["ip"] and j["ip"] != leaders[j["app"]]]
elif kind == "tenant-job":
    pool = [j for j in jobs if not j["system"] and j["app"] and j["app"] not in datastores
            and not j["app"].endswith("-plugin")]
else:
    raise SystemExit("unknown FAULT %s" % kind)
pool = [j for j in pool if j["id"]]
if app_match and kind.startswith("datastore-"):
    pool = [j for j in pool if app_match in j["app"]]
if not pool and unknown and any(not app_match or app_match in a for a in unknown):
    print("error: no %s job with a known leader (unknown: %s); not stopping a job" % (kind, ", ".join(sorted(unknown))))
    raise SystemExit(2)
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

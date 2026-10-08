#!/bin/bash
# Graceful job drain via the local host API (membership remove). Rolling
# reload does not call this: a VM reboot is a node failure, and remaining
# hosts plus cluster-monitor recover jobs.
set -euo pipefail

python3 - <<'PY'
import json, sys, time, urllib.error, urllib.parse, urllib.request

def load_key():
    try:
        with open("/etc/flynn/host.json") as f:
            env = (json.load(f) or {}).get("env") or {}
        return env.get("FLYNN_HOST_AUTH_KEY") or ""
    except FileNotFoundError:
        return ""

def req(method, path, key):
    url = "http://127.0.0.1:1113" + path
    r = urllib.request.Request(url, method=method)
    if key:
        r.add_header("Auth-Key", key)
    try:
        with urllib.request.urlopen(r, timeout=60) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()

def list_active(key):
    code, body = req("GET", "/host/jobs?active=true", key)
    if code != 200:
        sys.exit("list jobs failed HTTP %s: %s" % (code, body[:300]))
    data = json.loads(body.decode() or "{}")
    if isinstance(data, dict):
        return data
    if isinstance(data, list):
        out = {}
        for job in data:
            jid = ((job.get("job") or {}).get("id")) or job.get("id")
            if jid:
                out[jid] = job
        return out
    sys.exit("unexpected /host/jobs payload: %s" % type(data).__name__)

def is_discoverd(job):
    j = job.get("job") or {}
    md = j.get("metadata") or {}
    name = (md.get("flynn-controller.app_name") or "").lower()
    args = " ".join((j.get("config") or {}).get("args") or [])
    blob = " ".join([name, args, j.get("id") or ""])
    return "discoverd" in blob

def stop_one(jid, key):
    path = "/host/jobs/" + urllib.parse.quote(jid, safe="")
    code, body = req("DELETE", path, key)
    if code in (200, 404):
        print("stopped %s (HTTP %s)" % (jid, code))
        return
    print("stop %s failed HTTP %s: %s" % (jid, code, body[:200]), file=sys.stderr)

key = load_key()
stopped = 0
left = {}
for _pass in range(5):
    jobs = list_active(key)
    if not jobs:
        left = {}
        break
    first = [(jid, job) for jid, job in jobs.items() if not is_discoverd(job)]
    last = [(jid, job) for jid, job in jobs.items() if is_discoverd(job)]
    for jid, _job in first + last:
        stop_one(jid, key)
        stopped += 1
    time.sleep(1)
    left = list_active(key)
if left:
    print("jobs still active after drain (scheduler may have replaced them): %s" % ",".join(sorted(left)))
else:
    print("drained %d jobs" % stopped)
PY

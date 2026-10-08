#!/bin/bash
# After a node crash/reboot, wait until tenant HTTP apps answer again.
# Platform status can be "healthy" while app-one still has no web job (503).
# LIST_ONLY=1 prints app names (snapshot before a crash). TENANT_APPS pins
# that list so wait still covers apps whose jobs died with the host.
set -euo pipefail
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
IP="${CLUSTER_IP:-}"
TRIES="${TRIES:-60}"

python3 - "$DOMAIN" "${IP}" "${TRIES}" <<'PY'
import json, os, subprocess, sys, time, urllib.error, urllib.request

domain = sys.argv[1]
cluster_ip = sys.argv[2]
tries = int(sys.argv[3])

SYSTEM = {
    "blobstore", "controller", "discoverd", "flannel", "gitreceive",
    "logaggregator", "postgres", "router", "status", "tarreceive",
}

def load_env():
    with open("/etc/flynn/host.json") as f:
        return (json.load(f) or {}).get("env") or {}

def get(url, headers=None, timeout=5):
    r = urllib.request.Request(url)
    for k, v in (headers or {}).items():
        r.add_header(k, v)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:
        return 0, str(e).encode()

def tenant_apps(env):
    dk = env.get("DISCOVERD_AUTH_KEY") or ""
    hk = env.get("FLYNN_HOST_AUTH_KEY") or ""
    code, body = get("http://127.0.0.1:1111/services/flynn-host/instances", {"Auth-Key": dk})
    hosts = json.loads(body.decode() or "[]") if code == 200 else []
    names = set()
    for h in hosts:
        addr = (h.get("addr") or "").strip()
        if not addr:
            continue
        c, b = get("http://%s/host/jobs?active=true" % addr, {"Auth-Key": hk}, timeout=8)
        if c != 200:
            continue
        try:
            data = json.loads(b.decode() or "{}")
        except Exception:
            continue
        items = data.values() if isinstance(data, dict) else data
        for job in items or []:
            j = job.get("job") or job
            md = j.get("metadata") or {}
            if (md.get("flynn-system-app") or "").lower() == "true":
                continue
            name = (md.get("flynn-controller.app_name") or "").strip()
            if not name or name in SYSTEM or name.endswith("-plugin"):
                continue
            names.add(name)
    return sorted(names)

def curl_app(app):
    url = "https://%s.%s/" % (app, domain)
    cmd = ["curl", "-sk", "--max-time", "5", "-o", "/dev/null", "-w", "%{http_code}"]
    if cluster_ip:
        cmd += ["--resolve", "%s.%s:443:%s" % (app, domain, cluster_ip)]
    cmd.append(url)
    try:
        out = subprocess.check_output(cmd, stderr=subprocess.DEVNULL, timeout=8)
        code = out.decode().strip()
    except Exception:
        return False, "000"
    ok = code.isdigit() and 200 <= int(code) < 500 and code not in ("502", "503")
    return ok, code

env = load_env()
pinned = [a for a in os.environ.get("TENANT_APPS", "").split() if a]
apps = pinned or tenant_apps(env)
if os.environ.get("LIST_ONLY") == "1":
    for a in apps:
        print(a)
    raise SystemExit(0)
if not apps:
    print("no tenant HTTP apps to wait for")
    raise SystemExit(0)
print("waiting for tenant HTTP: %s" % ", ".join(apps))
last = {}
for i in range(1, tries + 1):
    bad = []
    for app in apps:
        ok, code = curl_app(app)
        last[app] = code
        if not ok:
            bad.append("%s=%s" % (app, code))
    if not bad:
        print("tenant HTTP ok: %s" % " ".join("%s=%s" % (a, last[a]) for a in apps))
        raise SystemExit(0)
    print("waiting for tenant HTTP (%s/%s): %s" % (i, tries, " ".join(bad)))
    time.sleep(5)
print("tenant HTTP still down: %s" % " ".join("%s=%s" % (a, last.get(a, "?")) for a in apps), file=sys.stderr)
raise SystemExit(1)
PY

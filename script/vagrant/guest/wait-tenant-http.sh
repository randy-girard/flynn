#!/bin/bash
# After a node crash/reboot, wait until tenant HTTP apps answer again.
# Platform status can be "healthy" while app-one still has no web job (503).
# LIST_ONLY=1 prints app names (snapshot before a crash). TENANT_APPS pins
# that list so wait still covers apps whose jobs died with the host.
# A pinned app that has no route any more (deleted while the snapshot was
# taken: the router fails TLS for its hostname and answers HTTP with 404) is
# dropped after GONE_TRIES consecutive rounds instead of timing out the wait.
set -euo pipefail
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
IP="${CLUSTER_IP:-}"
TRIES="${TRIES:-60}"
export HOST_JSON="${HOST_JSON:-/etc/flynn/host.json}"
export SLEEP="${SLEEP:-5}"
export GONE_TRIES="${GONE_TRIES:-6}"

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
    with open(os.environ["HOST_JSON"]) as f:
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
    # Tenant homepages can run a datastore check per attached resource before
    # the first byte. Five seconds marks a healthy demo as down.
    cmd = ["curl", "-sk", "--max-time", "20", "-o", "/dev/null", "-w", "%{http_code}"]
    if cluster_ip:
        cmd += ["--resolve", "%s.%s:443:%s" % (app, domain, cluster_ip)]
    cmd.append(url)
    try:
        out = subprocess.check_output(cmd, stderr=subprocess.DEVNULL, timeout=25)
        code = out.decode().strip()
    except Exception:
        return False, "000"
    ok = code.isdigit() and 200 <= int(code) < 500 and code not in ("502", "503")
    return ok, code

def no_route(app):
    # The router refuses TLS for a hostname it has no route for, so a
    # deleted app is 000 over HTTPS forever. Plain HTTP then gets the
    # router's 404.
    cmd = ["curl", "-s", "--max-time", "10", "-o", "/dev/null", "-w", "%{http_code}"]
    if cluster_ip:
        cmd += ["--resolve", "%s.%s:80:%s" % (app, domain, cluster_ip)]
    cmd.append("http://%s.%s/" % (app, domain))
    try:
        out = subprocess.check_output(cmd, stderr=subprocess.DEVNULL, timeout=15)
    except Exception:
        return False
    return out.decode().strip() == "404"

pinned = [a for a in os.environ.get("TENANT_APPS", "").split() if a]
apps = pinned or tenant_apps(load_env())
gone_tries = int(os.environ["GONE_TRIES"])
pause = float(os.environ["SLEEP"])
if os.environ.get("LIST_ONLY") == "1":
    for a in apps:
        print(a)
    raise SystemExit(0)
if not apps:
    print("no tenant HTTP apps to wait for")
    raise SystemExit(0)
print("waiting for tenant HTTP: %s" % ", ".join(apps))
last = {}
gone = {}
for i in range(1, tries + 1):
    bad = []
    for app in list(apps):
        ok, code = curl_app(app)
        last[app] = code
        if ok:
            gone[app] = 0
            continue
        if code == "000" and no_route(app):
            gone[app] = gone.get(app, 0) + 1
            if gone[app] >= gone_tries:
                print("%s has no route (deleted?); not waiting for it" % app)
                apps.remove(app)
                continue
        else:
            gone[app] = 0
        bad.append("%s=%s" % (app, code))
    if not bad:
        if apps:
            print("tenant HTTP ok: %s" % " ".join("%s=%s" % (a, last[a]) for a in apps))
        else:
            print("no tenant HTTP apps left to wait for")
        raise SystemExit(0)
    print("waiting for tenant HTTP (%s/%s): %s" % (i, tries, " ".join(bad)))
    time.sleep(pause)
print("tenant HTTP still down: %s" % " ".join("%s=%s" % (a, last.get(a, "?")) for a in apps), file=sys.stderr)
raise SystemExit(1)
PY

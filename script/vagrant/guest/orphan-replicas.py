#!/usr/bin/env python3
"""Fail when a finished datastore replica was left behind as a system app.

An image refresh or a broken-follower replace creates one isolated app
(postgresql-*, redis-*, mysql-*) that is not a resource yet. That app may
exist while the copy is still running. After the grace period, or when the
app is an unreferenced primary, it is a leftover and this check fails.

ORPHAN_REPLICA_SELFTEST=1 runs the decision cases and does not touch a cluster.
The live path reads the controller from /etc/flynn/host.json and prints only
app names, roles, and ages.
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# Matches postgres.DefaultUpgradeTimeout. A copy still running inside this
# window is the one in-progress replica, not a leftover.
GRACE_SECONDS = 30 * 60

ISOLATED = re.compile(
    r"^(?:"
    r"postgresql-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9]{5,8}"
    r"|pg-[a-z]+-[a-z0-9]{6,8}"
    r"|redis-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9]{5,8}"
    r"|mysql-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9]{5,8}"
    r")$"
)

ROLE_KEYS = (
    "FLYNN_POSTGRES_ROLE",
    "POSTGRES_ROLE",
    "FLYNN_REDIS_ROLE",
    "REDIS_ROLE",
    "FLYNN_MYSQL_ROLE",
    "MYSQL_ROLE",
)
LEADER_KEYS = (
    "FLYNN_POSTGRES_LEADER",
    "FLYNN_REDIS_LEADER",
    "FLYNN_MYSQL_LEADER",
)


def isolated_name(name):
    return bool(ISOLATED.match((name or "").strip()))


def host_app(value):
    value = (value or "").strip()
    if "://" not in value:
        return ""
    host = urllib.parse.urlsplit(value).hostname or ""
    if host.endswith(".discoverd"):
        host = host[: -len(".discoverd")]
    if host.startswith("leader."):
        host = host[len("leader.") :]
    return host


def referenced_names(resources):
    names = set()
    for res in resources or []:
        if not isinstance(res, dict):
            continue
        ext = (res.get("external_id") or "").strip()
        if ext:
            names.add(ext)
        for val in (res.get("env") or {}).values():
            text = (val or "").strip()
            if not text:
                continue
            names.add(text)
            host = host_app(text)
            if host:
                names.add(host)
    return names


def role_and_leader(env):
    env = env or {}
    role = ""
    for key in ROLE_KEYS:
        role = (env.get(key) or "").strip().lower()
        if role:
            break
    leader = ""
    for key in LEADER_KEYS:
        leader = (env.get(key) or "").strip()
        if leader:
            break
    return role, leader


def leftovers(apps, releases, referenced, now):
    """Return human-readable problems. releases maps app id to env."""
    young = {}
    problems = []
    pending = []
    for app in apps or []:
        if not isinstance(app, dict):
            continue
        name = (app.get("name") or "").strip()
        if not isolated_name(name) or name in referenced:
            continue
        env = releases.get(app.get("id") or "", {}) or {}
        role, leader = role_and_leader(env)
        created = app.get("created_at") or ""
        age = None
        if created:
            try:
                import calendar
                stamp = created.replace("Z", "+00:00")
                age = now - calendar.timegm(time.strptime(stamp[:19], "%Y-%m-%dT%H:%M:%S"))
            except ValueError:
                age = None
        if role == "follower" and age is not None and 0 <= age <= GRACE_SECONDS and leader in referenced:
            young.setdefault(leader, []).append(name)
            pending.append((name, leader, age))
            continue
        if role == "follower" and age is not None and age < 0:
            age = 0
        label = role or "unknown"
        if age is None:
            problems.append("%s role=%s age=unknown leader=%s" % (name, label, leader or "-"))
        else:
            problems.append("%s role=%s age=%ds leader=%s" % (name, label, int(age), leader or "-"))
    for leader, names in sorted(young.items()):
        if len(names) > 1:
            problems.append("leader %s has %d in-progress replicas (%s)" % (leader, len(names), ",".join(sorted(names))))
    return problems


def selftest():
    now = 1_700_000_000
    fresh = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now - 60))
    old = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now - GRACE_SECONDS - 60))
    apps = [
        {"id": "plat", "name": "postgres", "created_at": old},
        {"id": "plug", "name": "redis-plugin", "created_at": old},
        {"id": "kept", "name": "postgresql-glacier-47677", "created_at": old},
        {"id": "copy", "name": "postgresql-rock-42504", "created_at": fresh},
        {"id": "stale", "name": "postgresql-pine-93275", "created_at": old},
        {"id": "extra", "name": "mysql-kelp-21532", "created_at": fresh},
        {"id": "pair", "name": "redis-cove-36044", "created_at": fresh},
        {"id": "pair2", "name": "redis-fjord-92969", "created_at": fresh},
    ]
    releases = {
        "kept": {"FLYNN_POSTGRES_ROLE": "primary"},
        "copy": {"FLYNN_POSTGRES_ROLE": "follower", "FLYNN_POSTGRES_LEADER": "postgresql-glacier-47677"},
        "stale": {"FLYNN_POSTGRES_ROLE": "follower", "FLYNN_POSTGRES_LEADER": "postgresql-glacier-47677"},
        "extra": {"FLYNN_MYSQL_ROLE": "primary"},
        "pair": {"FLYNN_REDIS_ROLE": "follower", "FLYNN_REDIS_LEADER": "redis-timber-91192"},
        "pair2": {"FLYNN_REDIS_ROLE": "follower", "FLYNN_REDIS_LEADER": "redis-timber-91192"},
    }
    referenced = {"postgresql-glacier-47677", "redis-timber-91192"}
    got = leftovers(apps, releases, referenced, now)
    text = "\n".join(got)
    for needle in ("postgresql-pine-93275", "mysql-kelp-21532", "redis-cove-36044"):
        if needle not in text:
            raise SystemExit("selftest missed %s in %s" % (needle, text))
    if "postgresql-rock-42504" in text:
        raise SystemExit("selftest flagged the in-progress replica: %s" % text)
    if "redis-plugin" in text or "\npostgres " in text:
        raise SystemExit("selftest flagged a non-instance app: %s" % text)
    print("ok orphan-replica selftest")


def controller():
    env = json.load(open("/etc/flynn/host.json")).get("env") or {}
    key = env.get("CONTROLLER_KEY") or ""
    discoverd = {"Auth-Key": env.get("DISCOVERD_AUTH_KEY") or ""}
    auth = {
        "Authorization": "Basic " + __import__("base64").b64encode((":" + key).encode()).decode(),
        "Accept": "application/json",
    }

    def get_json(url, headers):
        req = urllib.request.Request(url, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=8) as res:
                return json.loads(res.read().decode() or "null")
        except urllib.error.HTTPError as err:
            raise SystemExit("controller HTTP %s" % err.code)

    instances = get_json("http://127.0.0.1:1111/services/controller/instances", discoverd) or []
    ctl = None
    for inst in instances:
        addr = (inst or {}).get("addr") or ""
        if not addr:
            continue
        try:
            get_json("http://%s/providers" % addr, auth)
            ctl = addr
            break
        except SystemExit:
            continue
    if not ctl:
        raise SystemExit("controller API unavailable")
    apps = get_json("http://%s/apps" % ctl, auth) or []
    resources = []
    for prov in get_json("http://%s/providers" % ctl, auth) or []:
        pid = (prov or {}).get("id") or ""
        if not pid:
            continue
        resources.extend(get_json("http://%s/providers/%s/resources" % (ctl, pid), auth) or [])
    referenced = referenced_names(resources)
    releases = {}
    for app in apps:
        name = (app or {}).get("name") or ""
        if not isolated_name(name) or name in referenced:
            continue
        app_id = app.get("id") or ""
        rel = get_json("http://%s/apps/%s/release" % (ctl, app_id), auth) or {}
        releases[app_id] = rel.get("env") or {}
    problems = leftovers(apps, releases, referenced, time.time())
    if problems:
        print("leftover datastore apps: " + "; ".join(problems))
        sys.exit(1)
    print("orphan replicas=0")


def main():
    if os.environ.get("ORPHAN_REPLICA_SELFTEST") == "1":
        selftest()
        return
    controller()


if __name__ == "__main__":
    try:
        main()
    except BrokenPipeError:
        sys.exit(0)

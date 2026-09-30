#!/bin/bash
# Run on cluster node1 as root. Prints labeled TLS pin and controller key so
# vagrant ssh motd/login banners cannot shift line 1/2 parsing.
#
# Prefer flynn-host cli-add-command (AUTH_KEY on the running controller job).
# Leftover /etc/flynn/host.json after a wiped cluster can still hold an older
# AUTH_KEY; cluster:add does not check that key (GET /ca-cert is TOFU), then
# flynn apps 401s.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
export FLYNN_ROOT="${ROOT}"
python3 - <<'PY'
import base64, hashlib, json, os, re, subprocess

ROOT = os.environ["FLYNN_ROOT"]
DOMAIN = (os.environ.get("FLYNN_CLUSTER_DOMAIN") or "1.localflynn.com").strip()
CONTROLLER = "https://controller.%s/apps" % DOMAIN


def host_only_ip():
    env_ip = (os.environ.get("FLYNN_CLUSTER_IP") or "").strip()
    if env_ip:
        return env_ip
    try:
        data = json.load(open("/etc/flynn/host.json")) or {}
    except Exception:
        data = {}
    env = data.get("env") or {}
    for key in ("EXTERNAL_IP", "FLYNN_EXTERNAL_IP"):
        v = str(env.get(key) or "").strip()
        if v:
            return v
    for prefix in ("192.168.57.", "192.168.56."):
        try:
            out = subprocess.check_output(["hostname", "-I"], text=True)
        except Exception:
            out = ""
        for tok in out.split():
            if tok.startswith(prefix):
                return tok
    return "127.0.0.1"


IP = host_only_ip()
RESOLVE = "controller.%s:443:%s" % (DOMAIN, IP)


def host_env():
    try:
        data = json.load(open("/etc/flynn/host.json")) or {}
    except Exception:
        return {}
    return data.get("env") or {}


def openssl_pin():
    raw = subprocess.check_output(
        "echo | openssl s_client -connect %s:443 -servername controller.%s 2>/dev/null | openssl x509 -outform DER"
        % (IP, DOMAIN),
        shell=True,
    )
    return base64.b64encode(hashlib.sha256(raw).digest()).decode()


def host_json_key(env):
    for name in ("CONTROLLER_KEY", "AUTH_KEY"):
        v = str(env.get(name) or "").strip()
        if v:
            return v
    return ""


def parse_cli_add(text):
    m = re.search(
        r"flynn cluster:add -p (\S+) \S+ \S+ (\S+)\s*$",
        text,
        re.M,
    )
    if not m:
        return "", ""
    return m.group(1).strip(), m.group(2).strip()


def cli_add_creds():
    env = os.environ.copy()
    disc = str(host_env().get("DISCOVERD") or "").strip()
    if disc and disc != "none":
        env["DISCOVERD"] = disc
    bins = [
        "/usr/local/bin/flynn-host",
        os.path.join(ROOT, "build/bin/flynn-host"),
        "/usr/local/libexec/flynn-host",
    ]
    last = None
    for bin_path in bins:
        if not os.path.isfile(bin_path):
            continue
        try:
            out = subprocess.check_output(
                [bin_path, "cli-add-command"],
                env=env,
                stderr=subprocess.STDOUT,
                text=True,
            )
        except (subprocess.CalledProcessError, OSError) as err:
            last = err
            continue
        pin, key = parse_cli_add(out)
        if pin and key:
            return pin, key
        last = RuntimeError("could not parse flynn-host cli-add-command output")
    if last:
        raise last
    raise RuntimeError("flynn-host binary not found for cli-add-command")


def apps_status(key):
    try:
        code = subprocess.check_output(
            [
                "curl", "-sk", "--max-time", "5",
                "-o", "/dev/null", "-w", "%{http_code}",
                "-u", ":" + key,
                "--resolve", RESOLVE,
                CONTROLLER,
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError:
        return ""
    return code


env = host_env()
pin = ""
key = ""
try:
    pin, key = cli_add_creds()
except Exception:
    pin = openssl_pin()
    key = host_json_key(env)

if not pin:
    try:
        pin = openssl_pin()
    except Exception:
        pass
if not key:
    key = host_json_key(env)

pin = (pin or "").strip()
key = (key or "").strip()
if not pin or not key:
    raise SystemExit("missing TLS pin or controller key (flynn-host cli-add-command / host.json)")

status = apps_status(key)
if status not in ("200",):
    alt = host_json_key(env)
    if alt and alt != key and apps_status(alt) == "200":
        key = alt
        status = "200"

if status == "401":
    raise SystemExit(
        "controller rejected the cluster key (HTTP 401) on GET /apps. "
        "Leftover /etc/flynn/host.json AUTH_KEY does not match the running controller job. "
        "Run: flynn-host cli-add-command"
    )

print("FLYNN_DEV_PIN=" + pin)
print("FLYNN_DEV_KEY=" + key)
PY

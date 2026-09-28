#!/bin/bash
# Run on the builder as root. Prints labeled TLS pin and controller key so
# vagrant ssh motd/login banners cannot shift line 1/2 parsing.
set -euo pipefail
python3 - <<'PY'
import json, subprocess, base64, hashlib
env = json.load(open("/etc/flynn/host.json"))["env"]
raw = subprocess.check_output(
    "echo | openssl s_client -connect 192.0.2.200:443 -servername controller.1.localflynn.com 2>/dev/null | openssl x509 -outform DER",
    shell=True,
)
pin = base64.b64encode(hashlib.sha256(raw).digest()).decode()
key = env.get("CONTROLLER_KEY") or env.get("AUTH_KEY") or ""
if not pin or not key:
    raise SystemExit("missing TLS pin or controller key in /etc/flynn/host.json")
print("FLYNN_DEV_PIN=" + pin)
print("FLYNN_DEV_KEY=" + key)
PY

#!/bin/bash
# Run on the builder as root. Prints the TLS pin, then the controller key.
set -euo pipefail
python3 - <<'PY'
import json, subprocess, base64, hashlib
env = json.load(open("/etc/flynn/host.json"))["env"]
raw = subprocess.check_output(
    "echo | openssl s_client -connect 192.0.2.200:443 -servername controller.1.localflynn.com 2>/dev/null | openssl x509 -outform DER",
    shell=True,
)
print(base64.b64encode(hashlib.sha256(raw).digest()).decode())
print(env["CONTROLLER_KEY"])
PY

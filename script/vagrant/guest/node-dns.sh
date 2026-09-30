#!/bin/bash
# Run on a cluster node as root. Point CLUSTER_DOMAIN at node1's host-only IP
# so controller TLS and flynn-host bootstrap can resolve in-guest.
set -euo pipefail
: "${CLUSTER_IP:?CLUSTER_IP is required}"
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
begin="# flynn-vagrant-dev-begin"
end="# flynn-vagrant-dev-end"
body="${CLUSTER_IP} ${DOMAIN} controller.${DOMAIN} git.${DOMAIN} images.${DOMAIN} dashboard.${DOMAIN} www.${DOMAIN} discovery.${DOMAIN} status.${DOMAIN}"

export FLYNN_HOSTS_BEGIN="${begin}"
export FLYNN_HOSTS_END="${end}"
export FLYNN_HOSTS_BODY="${body}"
python3 - <<'PY'
from pathlib import Path
import os

p = Path("/etc/hosts")
text = p.read_text() if p.exists() else ""
begin = os.environ["FLYNN_HOSTS_BEGIN"]
end = os.environ["FLYNN_HOSTS_END"]
body = os.environ["FLYNN_HOSTS_BODY"].rstrip() + "\n"
lines = text.splitlines(True)
out = []
i = 0
while i < len(lines):
    s = lines[i].strip()
    if s == begin:
        i += 1
        while i < len(lines) and lines[i].strip() != end:
            i += 1
        if i < len(lines):
            i += 1
        continue
    if "flynn-vagrant-dev" in lines[i]:
        i += 1
        continue
    out.append(lines[i])
    i += 1
out.append(begin + "\n")
out.append(body)
out.append(end + "\n")
p.write_text("".join(out))
PY
echo "hosts: ${body}"

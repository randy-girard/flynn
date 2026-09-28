#!/bin/bash
# Install flynn-host on the guest PATH and point DISCOVERD at the TEST-NET
# listen IP. bootstrap-flynn binds discoverd on 192.0.2.200:1111, not
# 127.0.0.1:1111, so a bare `flynn-host ps` otherwise fails with connection
# refused. Run on the builder as root.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
src="${ROOT}/build/bin/flynn-host"
if [[ ! -x "${src}" ]]; then
  echo "flynn-host is not at ${src}; build the cluster first" >&2
  exit 1
fi
mkdir -p /usr/local/libexec
install -m 0755 "${src}" /usr/local/libexec/flynn-host

python3 - <<'PY'
import json, os
path = "/etc/flynn/host.json"
env = {}
data = {}
if os.path.isfile(path):
    with open(path) as f:
        data = json.load(f) or {}
    env = data.get("env") or {}
disc = (env.get("DISCOVERD") or "").strip()
if not disc or disc == "none":
    env["DISCOVERD"] = "192.0.2.200:1111"
    data["env"] = env
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(data, f, indent="\t")
        f.write("\n")
    os.chmod(path, 0o600)
PY

cat > /usr/local/bin/flynn-host <<'EOF'
#!/bin/bash
# Wrapper: the real binary is /usr/local/libexec/flynn-host.
# Export DISCOVERD before exec so even an older binary (package-init
# DefaultClient) talks to 192.0.2.200:1111 instead of loopback.
set -euo pipefail
if [[ -z "${DISCOVERD:-}" || "${DISCOVERD}" == "none" ]]; then
  if [[ -f /etc/flynn/host.json ]]; then
    d="$(python3 -c 'import json
e=(json.load(open("/etc/flynn/host.json")) or {}).get("env") or {}
print((e.get("DISCOVERD") or "").strip())' 2>/dev/null || true)"
    if [[ -n "${d}" && "${d}" != "none" ]]; then
      export DISCOVERD="${d}"
    fi
  fi
fi
if [[ -z "${DISCOVERD:-}" || "${DISCOVERD}" == "none" ]]; then
  export DISCOVERD=192.0.2.200:1111
fi
exec /usr/local/libexec/flynn-host "$@"
EOF
chmod 0755 /usr/local/bin/flynn-host
echo "installed $(command -v flynn-host) -> /usr/local/libexec/flynn-host DISCOVERD=${DISCOVERD:-from-wrapper}"

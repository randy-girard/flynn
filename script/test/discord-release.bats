#!/usr/bin/env bats

load "helper"
load_lib "discord-release.sh"

setup() {
  TMP="$(mktemp -d "${BATS_TMPDIR}/discord-release.XXXXXX")"
  POSTS="${TMP}/posts"
  : >"${POSTS}"
  discord_release_http_post() {
    printf '%s\n' "$1" >>"${POSTS}.url"
    printf '%s\n' "$2" >>"${POSTS}"
    printf '204'
  }
}

teardown() {
  rm -rf "${TMP}"
}

@test "discord_notify_github_release skips when webhook is unset" {
  unset DISCORD_RELEASE_CHANNEL_WEBHOOK_URL
  echo "feat(router): faster TTFB" >"${TMP}/notes.md"
  run discord_notify_github_release \
    --repo acme/flynn \
    --version v20990101.0 \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --draft false \
    --prerelease false
  assert_success
  [[ "${output}" == *"skipped"* ]]
  if [[ -s "${POSTS}" ]]; then
    echo "must not POST when webhook is unset" >&2
    return 1
  fi
}

@test "discord_notify_github_release skips drafts" {
  export DISCORD_RELEASE_CHANNEL_WEBHOOK_URL="https://discord.com/api/webhooks/1/abc"
  echo "notes" >"${TMP}/notes.md"
  run discord_notify_github_release \
    --repo acme/flynn \
    --version v20990101.0 \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --draft true \
    --prerelease false
  assert_success
  [[ "${output}" == *"draft"* ]]
  if [[ -s "${POSTS}" ]]; then
    echo "must not POST drafts" >&2
    return 1
  fi
}

@test "discord_notify_github_release posts changelog only without install or artifacts" {
  export DISCORD_RELEASE_CHANNEL_WEBHOOK_URL="https://discord.com/api/webhooks/1/abc"
  cat >"${TMP}/notes.md" <<'EOF'
## Flynn v20990101.0

**Full Changelog**: [v20260923.0...v20990101.0](https://github.com/acme/flynn/compare/v20260923.0...v20990101.0)

### ✨ Features

- feat(cli): show plugin help (abc123)

---

## Install Flynn CLI

Install the Flynn command-line interface on your local machine (Linux or macOS):

```bash
curl -fsSL https://github.com/acme/flynn/releases/download/v20990101.0/install-flynn-cli | sudo bash
```

## Install Flynn Host (Server)

Install Flynn on an Ubuntu server (24.04):

```bash
curl -fsSL https://github.com/acme/flynn/releases/download/v20990101.0/install-flynn | sudo bash
```

## Artifacts

### CLI Binaries
| Binary | Platform |
|--------|----------|
| flynn-linux-amd64.gz | Linux (x86_64) |

### Other
| File | Description |
|------|-------------|
| install-flynn-cli | CLI installer script |
| layer.squashfs | rootfs layer |
EOF
  run discord_notify_github_release \
    --repo acme/flynn \
    --version v20990101.0 \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --draft false \
    --prerelease false
  assert_success
  [[ "${output}" == *"posted"* ]]
  [[ "$(cat "${POSTS}.url")" == "https://discord.com/api/webhooks/1/abc" ]]
  python3 - "${POSTS}" <<'PY'
import json, sys
payload = json.loads(open(sys.argv[1]).read())
blob = json.dumps(payload)
desc = payload["embeds"][0]["description"]
assert "Flynn v20990101.0" in desc
assert "Full Changelog" in desc
assert "v20260923.0...v20990101.0" in desc
assert "feat(cli): show plugin help" in desc
assert "Install Flynn CLI" not in desc
assert "Install Flynn Host" not in desc
assert "install-flynn" not in blob
assert "curl -fsSL" not in blob
assert "## Artifacts" not in desc
assert "flynn-linux-amd64.gz" not in blob
assert "layer.squashfs" not in blob
assert payload["embeds"][0]["url"] == "https://github.com/acme/flynn/releases/tag/v20990101.0"
assert "v20990101.0" in payload["content"]
assert payload["embeds"][0]["url"] in payload["content"]
PY
}

@test "discord_notify_github_release strips plugin install image.json and layers" {
  export DISCORD_RELEASE_CHANNEL_WEBHOOK_URL="https://discord.com/api/webhooks/1/abc"
  cat >"${TMP}/notes.md" <<'EOF'
## redis v20990101.0

**Full Changelog**: [v20260923.0...v20990101.0](https://github.com/acme/flynn-plugin-redis/compare/v20260923.0...v20990101.0)

### ✨ Features

- feat(redis): faster dump (abc123)

---

## Install

On a Flynn cluster host:

```text
sudo flynn-host plugin install redis --ref v20990101.0
```

Or from this repository:

```text
sudo flynn-host plugin install https://github.com/acme/flynn-plugin-redis.git --ref v20990101.0
```

`flynn-host plugin install` pulls `image.json` from this release:

https://github.com/acme/flynn-plugin-redis/releases/download/v20990101.0/image.json

Layers are Flynn ubuntu-noble plus this plugin's delta (`{id}.squashfs` next to that Artifact).
EOF
  run discord_notify_github_release \
    --repo acme/flynn-plugin-redis \
    --version v20990101.0 \
    --title "redis v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --draft false \
    --prerelease false
  assert_success
  python3 - "${POSTS}" <<'PY'
import json, sys
payload = json.loads(open(sys.argv[1]).read())
blob = json.dumps(payload)
desc = payload["embeds"][0]["description"]
assert "redis v20990101.0" in desc
assert "Full Changelog" in desc
assert "feat(redis): faster dump" in desc
assert "## Install" not in desc
assert "plugin install" not in blob
assert "image.json" not in blob
assert "ubuntu-noble" not in blob
assert ".squashfs" not in blob
PY
}

@test "discord_notify_github_release marks prereleases" {
  export DISCORD_RELEASE_CHANNEL_WEBHOOK_URL="https://discord.com/api/webhooks/1/abc"
  echo "notes" >"${TMP}/notes.md"
  run discord_notify_github_release \
    --repo acme/flynn \
    --version v20990101.0 \
    --title "Flynn v20990101.0" \
    --notes-file "${TMP}/notes.md" \
    --draft false \
    --prerelease true
  assert_success
  python3 - "${POSTS}" <<'PY'
import json, sys
payload = json.loads(open(sys.argv[1]).read())
assert payload["embeds"][0]["title"].startswith("Prerelease:")
PY
}

@test "release workflow passes DISCORD_RELEASE_CHANNEL_WEBHOOK_URL into publish" {
  wf="${ROOT}/.github/workflows/release.yml"
  grep -A4 'name: Create GitHub Release' "${wf}" | grep -q 'DISCORD_RELEASE_CHANNEL_WEBHOOK_URL'
  grep -q 'vars.DISCORD_RELEASE_CHANNEL_WEBHOOK_URL' "${wf}"
}

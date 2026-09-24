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

@test "discord_notify_github_release posts title notes and url without assets" {
  export DISCORD_RELEASE_CHANNEL_WEBHOOK_URL="https://discord.com/api/webhooks/1/abc"
  printf '%s\n' "### CLI" "- feat(cli): show plugin help" >"${TMP}/notes.md"
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
assert "install-flynn" not in json.dumps(payload)
assert "layer.squashfs" not in json.dumps(payload)
assert payload["embeds"][0]["url"] == "https://github.com/acme/flynn/releases/tag/v20990101.0"
assert "feat(cli): show plugin help" in payload["embeds"][0]["description"]
assert "v20990101.0" in payload["content"]
assert payload["embeds"][0]["url"] in payload["content"]
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

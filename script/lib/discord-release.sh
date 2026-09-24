# Post a Discord channel message after a GitHub Release is published.
#
# Reads DISCORD_RELEASE_CHANNEL_WEBHOOK_URL (Actions variable or secret).
# Skips when the URL is empty or the release is still a draft. Posts the
# title, grouped change notes, and the html release URL. Does not list
# GitHub Release assets. A Discord failure is logged and does not fail
# the GitHub publish.
#
# shellcheck shell=bash

# Overridable in tests.
discord_release_http_post() {
  local webhook=$1
  local payload=$2
  curl -sS -o /dev/null -w "%{http_code}" -X POST \
    "${webhook}" \
    -H "Content-Type: application/json" \
    --data-binary "${payload}"
}

discord_release_html_url() {
  local repo=$1 version=$2
  printf 'https://github.com/%s/releases/tag/%s' "${repo}" "${version}"
}

# args: title, url, prerelease true|false, notes-file
discord_release_payload() {
  local title=$1 url=$2 prerelease=$3 notes_file=$4
  python3 - "${title}" "${url}" "${prerelease}" "${notes_file}" <<'PY'
import json, sys

title, url, prerelease, notes_file = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
notes = open(notes_file, encoding="utf-8").read().replace("\r\n", "\n").strip()
if not notes:
    notes = "See the GitHub release for details."
# Embed description limit is 4096; keep headroom for Discord markdown.
limit = 3500
if len(notes) > limit:
    notes = notes[: limit - 1].rstrip() + "…"

heading = title
if prerelease == "true":
    heading = "Prerelease: " + title

# content is the short ping; notes live in the embed (not asset lists).
content = f"**{heading}**\n{url}"
if len(content) > 2000:
    content = content[:1999] + "…"

print(json.dumps({
    "content": content,
    "embeds": [{
        "title": heading[:256],
        "url": url,
        "description": notes,
        "color": 5793266,
    }],
    "allowed_mentions": {"parse": []},
}))
PY
}

# discord_notify_github_release --repo OWNER/REPO --version TAG --title TITLE \
#   --notes-file FILE [--draft true|false] [--prerelease true|false] [--url URL]
discord_notify_github_release() {
  local repo="" version="" title="" notes="" draft="false" prerelease="false" url=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --repo) repo=$2; shift 2 ;;
      --version) version=$2; shift 2 ;;
      --title) title=$2; shift 2 ;;
      --notes-file) notes=$2; shift 2 ;;
      --draft) draft=$2; shift 2 ;;
      --prerelease) prerelease=$2; shift 2 ;;
      --url) url=$2; shift 2 ;;
      *)
        echo "unknown discord_notify_github_release argument: $1" >&2
        return 1
        ;;
    esac
  done
  if [[ -z "${repo}" || -z "${version}" || -z "${title}" || -z "${notes}" ]]; then
    echo "discord_notify_github_release requires --repo --version --title --notes-file" >&2
    return 1
  fi

  local webhook="${DISCORD_RELEASE_CHANNEL_WEBHOOK_URL:-}"
  webhook="${webhook//[$'\t\r\n ']/}"
  if [[ -z "${webhook}" ]]; then
    echo "Discord release notify skipped (DISCORD_RELEASE_CHANNEL_WEBHOOK_URL unset)"
    return 0
  fi
  if [[ "${draft}" == "true" ]]; then
    echo "Discord release notify skipped (draft)"
    return 0
  fi
  if [[ ! -f "${notes}" ]]; then
    echo "Discord release notify skipped (notes file missing)" >&2
    return 0
  fi
  if [[ -z "${url}" ]]; then
    url="$(discord_release_html_url "${repo}" "${version}")"
  fi

  local payload
  if ! payload="$(discord_release_payload "${title}" "${url}" "${prerelease}" "${notes}")"; then
    echo "warning: Discord release payload failed" >&2
    return 0
  fi

  local code
  code="$(discord_release_http_post "${webhook}" "${payload}")" || code="000"
  case "${code}" in
    200|204)
      echo "Discord release notify posted (${code}) ${url}"
      ;;
    *)
      echo "warning: Discord release notify HTTP ${code} (release ${version} is still published)" >&2
      ;;
  esac
  return 0
}

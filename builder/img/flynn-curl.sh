#!/bin/bash
# Flynn-builder HTTP GET cache: bodies live under ${FLYNN_HTTP_CACHE_ROOT} (_flynn_http/ on host).
# Delegates non-cacheable invocations to the real curl.
set -euo pipefail

REAL_CURL="${REAL_CURL:-/usr/bin/curl}"
exec_real() { exec "$REAL_CURL" "$@"; }

ROOT="${FLYNN_HTTP_CACHE_ROOT:-}"
[[ -z "${ROOT}" || ! -d "${ROOT}" ]] && exec_real "$@"
mkdir -p "${ROOT}"

for a in "$@"; do
  case "${a}" in
  --data | --data-* | --upload-file | --form | -d | \
    --range | --continue-at | --etag-* | --time-cond* | \
    --remote-name | --remote-header-name | \
    -I | --head | -O* | \
    --config | -K | --unix-socket | \
    --digest | --ntlm | --negotiate | \
    -b | --cookie | -c | --cookie-jar | \
    -u | --proxy-user | --proxy | --cert | -E | \
    -x | --socks* | \
    --pubkey | --header | --aws-* | \
    -[T])
    exec_real "$@"
    ;;
  esac

  [[ "${a}" =~ ^-d ]] && [[ "${a}" != "-d" ]] && exec_real "$@"
done

for ((idx = 1; idx <= $#; idx++)); do
  a="${!idx}"
  case "${a}" in
  -X | --request)
    [[ $((idx + 1)) -le $# ]] || exec_real "$@"
    m="$(tr '[:upper:]' '[:lower:]' <<<"${@:$((idx + 1)):1}")"
    [[ "${m}" == "get" ]] || exec_real "$@"
    ;;
  --request=*)
    m="$(tr '[:upper:]' '[:lower:]' <<<"${a#*=}")"
    [[ "${m}" == "get" ]] || exec_real "$@"
    ;;
  esac
done

urls=()
for arg in "$@"; do
  [[ "${arg}" =~ ^[a-zA-Z][a-zA-Z0-9+.-]*:// ]] && urls+=("${arg}")

done


[[ "${#urls[@]}" -eq 1 ]] || exec_real "$@"
THEURL="${urls[0]}"

url_idx=""
for ((k = 1; k <= $#; k++)); do
  [[ "${!k}" == "${THEURL}" ]] && url_idx="$k" && break
done
[[ -n "${url_idx}" ]] || exec_real "$@"

had_o=false
out_path=""
next=""
for ((m = 1; m < url_idx; m++)); do
  a="${!m}"
  if [[ -n "${next}" ]]; then

    [[ "${next}" == "out" ]] && {
      had_o=true
      out_path="${a}"
    }
    next=""
    continue


  fi

  case "${a}" in


  --output=*)
    had_o=true


    out_path="${a#*=}"
    ;;
  --output)
    next="out"


    ;;
  -*)
    # Short options / merged -fsSLo/path and -o/path
    if [[ "${a}" != --* ]] && [[ "${a}" =~ ^-[A-Za-z0-9#]+o/(.+)$ ]]; then
      had_o=true
      out_path="/${BASH_REMATCH[1]}"
    elif [[ "${a}" =~ ^-o/(.+)$ ]]; then
      had_o=true
      out_path="/${BASH_REMATCH[1]}"
    elif [[ "${a}" == "-o" ]]; then
      next="out"
    elif [[ "${a}" != --* ]] && [[ "${a}" =~ ^-[A-Za-z0-9#]+o$ ]]; then
      next="out"
    elif [[ "${a}" == -o* ]]; then
      rest="${a#-o}"
      if [[ -z "${rest}" ]]; then

        next="out"
      else
        had_o=true
        out_path="${rest}"
      fi
    fi
    ;;
  esac
done

[[ -z "${next}" ]] || exec_real "$@"

rem=$(( $# - url_idx ))
if [[ "${had_o}" == false ]]; then
  if ((rem == 0)); then
    :
  elif ((rem == 1)) && [[ "${@:$((url_idx + 1)):1}" == --output=* ]]; then
    had_o=true
    _uo="${@:$((url_idx + 1)):1}"
    out_path="${_uo#*=}"
  elif ((rem == 2)) && [[ "${@:$((url_idx + 1)):1}" == "-o" ]]; then
    had_o=true
    out_path="${@:$((url_idx + 2)):1}"
  elif ((rem == 2)) && [[ "${@:$((url_idx + 1)):1}" == "--output" ]]; then
    had_o=true
    out_path="${@:$((url_idx + 2)):1}"
  else
    exec_real "$@"
  fi
elif ((rem != 0)); then
  # Output already chosen before URL; nothing may follow URL.
  exec_real "$@"

fi




[[ "${had_o}" == false || -n "${out_path}" ]] || exec_real "$@"


partial="${ROOT}/partial"
mkdir -p "${partial}"


key="$({ printf '%s' "${THEURL}" | sha256sum | awk '{print $1}'; })"
blob="${ROOT}/${key}.blob"


if [[ -f "${blob}" ]]; then



  if [[ "${had_o}" == true ]]; then
    mkdir -p "$(dirname "${out_path}")"
    cp -p -- "${blob}" "${out_path}"
  else
    cat "${blob}"
  fi
  exit 0
fi




if [[ "${had_o}" == true ]]; then
  "${REAL_CURL}" "$@"
  mkdir -p "$(dirname "${out_path}")"
  cp -p -- "${out_path}" "${blob}"
  exit 0
fi




tmp="${partial}/${key}.${BASHPID}.part"


pre=( "${@:1:url_idx}" )




"${REAL_CURL}" "${pre[@]}" -o "${tmp}" "${THEURL}"




mv -f "${tmp}" "${blob}"
cat "${blob}"

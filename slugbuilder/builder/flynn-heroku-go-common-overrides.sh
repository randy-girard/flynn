# --- Flynn: multi-arch overrides (appended to heroku-buildpack-go lib/common.sh) ---
# Redefines downloadFile + SHAValid so arm64 slugbuilders fetch linux-arm64 (or
# linux_arm64) URLs derived from files.json instead of amd64-only Heroku bucket
# keys, and relaxes SHA checks for those cross-arch substitutions.

flynn_host_is_arm64() {
  case "$(uname -m)" in
    aarch64|arm64) return 0 ;;
    *) return 1 ;;
  esac
}

flynn_release_download_url() {
  local fileName="$1"
  if ! flynn_host_is_arm64; then
    echo "${BucketURL}/${fileName}"
    return
  fi
  local u
  u="$(jq -r --arg k "${fileName}" '.[$k].URL // empty' "${FilesJSON}" 2>/dev/null || echo "")"
  if [[ -n "${u}" ]]; then
    case "${u}" in
      *linux-amd64*)
        echo "${u//linux-amd64/linux-arm64}"
        return
        ;;
      *linux_amd64*)
        echo "${u//linux_amd64/linux_arm64}"
        return
        ;;
    esac
    echo "${u}"
    return
  fi
  echo "${BucketURL}/${fileName}"
}

flynn_arm64_loose_integrity() {
  local fileName="$1"
  local targetFile="$2"
  [[ -s "${targetFile}" ]] || return 1
  if [[ "${fileName}" == *.tar.gz ]] || [[ "${targetFile}" == *.tar.gz ]]; then
    tar tzf "${targetFile}" >/dev/null 2>&1 || return 1
    return 0
  fi
  case "$(file -b "${targetFile}")" in
    *shell\ script*|*POSIX\ shell*|*ASCII\ text*|*UTF-8\ Unicode\ text*|*Unicode\ text*) return 0 ;;
    *aarch64*|*ARM\ aarch64*|*arm64*) return 0 ;;
    *x86-64*|*386*) return 1 ;;
    *) return 1 ;;
  esac
}

flynn_skip_shasum_for_arm64_cross() {
  flynn_host_is_arm64 || return 1
  local u
  u="$(jq -r --arg k "${1}" '.[$k].URL // empty' "${FilesJSON}" 2>/dev/null || echo "")"
  [[ -n "${u}" ]] || return 1
  [[ "${u}" == *linux-amd64* || "${u}" == *linux_amd64* ]] || return 1
  return 0
}

downloadFile() {
  local fileName="${1}"

  if ! knownFile ${fileName}; then
    err ""
    err "The requested file (${fileName}) is unknown to the buildpack!"
    err ""
    err "The buildpack tracks and validates the SHA256 sums of the files"
    err "it uses. Because the buildpack doesn't know about the file"
    err "it likely won't be able to obtain a copy and validate the SHA."
    err ""
    err "To find out more info about this error please visit:"
    err " https://devcenter.heroku.com/articles/unknown-go-buildack-files"
    err ""
    exit 1
  fi

  local targetDir="${2}"
  local xCmd="${3}"
  local localName="$(determinLocalFileName "${fileName}")"
  local targetFile="${targetDir}/${localName}"

  mkdir -p "${targetDir}"
  pushd "${targetDir}" &> /dev/null
  start "Fetching ${localName}"
  local _flynn_u
  _flynn_u="$(flynn_release_download_url "${fileName}")"
  ${CURL} -o "${fileName}" "${_flynn_u}"
  if [ "${fileName}" != "${localName}" ]; then
    mv "${fileName}" "${localName}"
  fi
  if [ -n "${xCmd}" ]; then
    ${xCmd} ${targetFile}
  fi
  if ! SHAValid "${fileName}" "${targetFile}"; then
    err ""
    err "Downloaded file (${fileName}) sha does not match recorded SHA"
    err "Unable to continue."
    err ""
    exit 1
  fi
  finished
  popd &> /dev/null
}

SHAValid() {
  local fileName="${1}"
  local targetFile="${2}"
  if flynn_skip_shasum_for_arm64_cross "${fileName}"; then
    flynn_arm64_loose_integrity "${fileName}" "${targetFile}"
    return $?
  fi
  local sh=""
  local sw="$(<"${FilesJSON}" jq -r '."'${fileName}'".SHA')"
  if [ ${#sw} -eq 40 ]; then
    sh="$(shasum "${targetFile}" | awk '{print $1}')"
  else
    sh="$(shasum -a256 "${targetFile}" | awk '{print $1}')"
  fi
  [ "${sh}" = "${sw}" ]
}

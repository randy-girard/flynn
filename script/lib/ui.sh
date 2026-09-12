# Shell UI helpers (POSIX-portable color).
#
# Never use `echo -e '\e[...m'`:
#   - macOS bash 3.2 does not treat \e as ESC (prints the two characters \e)
#   - some /bin/echo implementations ignore -e
# Always emit SGR via printf '\033[...m' or tput (terminfo).
#
# usage:
#   say "foo"
#   say "bar" "red"
#   say "baz" "cyan"
#   info "STEP: ..."
#   ui_session_begin / ui_session_end   # scoped theme for one script
#
# Disable: NO_COLOR=1
# Force:   CLICOLOR_FORCE=1  or  FORCE_COLOR=1

say() {
  local msg=$1
  local color=$2
  # Print wrap directly. Capturing wrap in command substitution makes stdout a
  # pipe, [[ -t 1 ]] fails, and a ui_session leaves everything in body white.
  ui_wrap "${color}" "${msg}"
  printf '\n'
  # Collapsed smoke steps redirect stdout to a log. Still show STEP/WARN/OK on
  # the terminal; command chatter stays in the log until the user expands it.
  if [[ "${_UI_SESSION:-0}" == "1" && "${_UI_COLLAPSE_BODY:-0}" == "1" ]] \
    && [[ ! -t 1 ]]; then
    # Open /dev/tty in a subshell first: macOS has the node with no controlling
    # terminal, and a direct >/dev/tty prints "Device not configured".
    if (exec >/dev/tty) 2>/dev/null; then
      { ui_wrap "${color}" "${msg}"; printf '\n'; } >/dev/tty 2>/dev/null || true
    fi
  fi
}

# Paint text without a newline. Restores session body (or sgr0) after the span.
ui_wrap() {
  local color=$1
  local text=$2
  local prefix="" suffix=""

  if [[ -n "${color}" ]] && ui_use_color; then
    ui_load_color_seqs
    case "${color}" in
      red)     prefix="${_UI_RED}" ;;
      green)   prefix="${_UI_GREEN}" ;;
      yellow)  prefix="${_UI_YELLOW}" ;;
      cyan)    prefix="${_UI_CYAN}" ;;
      magenta) prefix="${_UI_MAGENTA}" ;;
    esac
    if [[ -n "${prefix}" ]]; then
      if [[ "${_UI_SESSION:-0}" == "1" ]]; then
        suffix="${_UI_BODY}"
      else
        suffix="${_UI_RESET}"
      fi
    fi
  fi
  printf '%s%s%s' "${prefix}" "${text}" "${suffix}"
}

# Pad then color a PASS/FAIL/SKIP cell so table columns stay aligned.
ui_status_text() {
  local status=$1
  local padded
  padded="$(printf '%-6s' "${status}")"
  case "${status}" in
    PASS) ui_wrap green "${padded}" ;;
    FAIL) ui_wrap red "${padded}" ;;
    SKIP) ui_wrap yellow "${padded}" ;;
    *) printf '%s' "${padded}" ;;
  esac
}

# Color only if this function's fd 1 is a terminal (after redirects, so
# `say ... >&2` checks stderr). Same rule on macOS, Linux, and BSD.
ui_use_color() {
  if [[ -n "${NO_COLOR:-}" ]]; then
    return 1
  fi
  case "${FORCE_COLOR:-${CLICOLOR_FORCE:-}}" in
    1|true|TRUE|yes|YES) return 0 ;;
    0|false|FALSE|no|NO) return 1 ;;
  esac
  # ui_session_begin already saw a color-capable terminal. Keep coloring after
  # stdout is piped (run_step | tee) or captured ($(ui_status_text)).
  if [[ "${_UI_SESSION_COLOR:-0}" == "1" ]]; then
    return 0
  fi
  case "${TERM:-}" in
    dumb|dumb-*|'')
      if [[ -z "${TERM:-}" ]]; then
        :
      else
        return 1
      fi
      ;;
  esac
  # -t 2: $(ui_wrap) / $(ui_status_text) make fd 1 a pipe; stderr is still a TTY.
  [[ -t 1 || -t 2 ]]
}

# Load once. Prefer terminfo (Linux/macOS/BSD). Fall back to ECMA-48 SGR.
# 16-color bright codes (9x) are used for the smoke session so banners are not
# the same green many terminals use as the default foreground.
ui_load_color_seqs() {
  if [[ -n "${_UI_SEQS_READY:-}" ]]; then
    return 0
  fi
  _UI_SEQS_READY=1
  _UI_RED=""
  _UI_GREEN=""
  _UI_YELLOW=""
  _UI_CYAN=""
  _UI_MAGENTA=""
  _UI_BODY=""
  _UI_RESET=""

  if command -v tput >/dev/null 2>&1 \
    && tput setaf 6 >/dev/null 2>&1; then
    _UI_RED="$( { tput bold; tput setaf 1; } 2>/dev/null || true )"
    _UI_GREEN="$( { tput bold; tput setaf 2; } 2>/dev/null || true )"
    _UI_YELLOW="$( { tput bold; tput setaf 3; } 2>/dev/null || true )"
    _UI_CYAN="$( { tput bold; tput setaf 6; } 2>/dev/null || true )"
    _UI_MAGENTA="$( { tput bold; tput setaf 5; } 2>/dev/null || true )"
    _UI_RESET="$( tput sgr0 2>/dev/null || true )"
  fi

  if [[ -z "${_UI_CYAN}" || -z "${_UI_RESET}" ]]; then
    _UI_RED="$(printf '\033[1;91m')"
    _UI_GREEN="$(printf '\033[1;32m')"
    _UI_YELLOW="$(printf '\033[1;93m')"
    _UI_CYAN="$(printf '\033[1;96m')"
    _UI_MAGENTA="$(printf '\033[1;95m')"
    _UI_RESET="$(printf '\033[0m')"
  fi
  # Normal-weight bright white: sticky default for a ui_session (not sgr0,
  # which would restore the user's green profile foreground).
  _UI_BODY="$(printf '\033[0;22;97m')"
}

# POSIX %H:%M:%S works on GNU, BSD, and BusyBox date.
ui_timestamp() {
  date +%H:%M:%S
}

# Scoped theme for one script: body text is bright white (normal weight);
# info banners are bold cyan (not green). Restores the terminal on end/EXIT.
ui_session_begin() {
  _UI_SESSION=1
  _UI_SESSION_COLOR=0
  if ! ui_use_color; then
    return 0
  fi
  _UI_SESSION_COLOR=1
  ui_load_color_seqs
  printf '%s' "${_UI_BODY}"
}

ui_session_end() {
  if [[ "${_UI_SESSION:-0}" != "1" ]]; then
    return 0
  fi
  _UI_SESSION=0
  _UI_SESSION_COLOR=0
  if [[ -z "${NO_COLOR:-}" ]]; then
    printf '\033[0m'
  fi
}

info() {
  local msg=$1
  # Cyan during a session so STEP lines are not lost on a green default fg.
  if [[ "${_UI_SESSION:-0}" == "1" ]]; then
    say "===> $(ui_timestamp) ${msg}" "cyan"
  else
    say "===> $(ui_timestamp) ${msg}" "green"
  fi
}

ok() {
  local msg=$1
  say "===> $(ui_timestamp) ${msg}" "green"
}

warn() {
  local msg=$1
  say "===> $(ui_timestamp) WARN: ${msg}" "yellow" >&2
}

fail() {
  local msg=$1
  say "ERROR: ${msg}" "red" >&2
  exit 1
}

ui_banner() {
  say "$1" "cyan"
}

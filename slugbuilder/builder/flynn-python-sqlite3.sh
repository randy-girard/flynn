#!/usr/bin/env bash
# Flynn replacement for heroku-buildpack-python bin/steps/sqlite3.
# The upstream step forces APT onto /etc/apt/sources.list (empty on Ubuntu 24.04)
# and then mv's usr/lib/$(arch)-linux-gnu. On aarch64 that is aarch64-linux-gnu;
# with no debs extracted, mv/sed fail. Use the stack's libsqlite3 and never
# call mv with an empty glob.
#
# Classic compile sources this file; always run the install step.

sqlite3_gnu_triplet() {
  local t
  t="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
  if [[ -n "${t}" ]]; then
    printf '%s' "${t}"
    return
  fi
  case "$(arch 2>/dev/null || uname -m)" in
    aarch64 | arm64) printf '%s' aarch64-linux-gnu ;;
    x86_64 | amd64) printf '%s' x86_64-linux-gnu ;;
    *) printf '%s' x86_64-linux-gnu ;;
  esac
}

sqlite3_stack_libdir() {
  local triplet="$1"
  local d
  for d in "/usr/lib/${triplet}" /usr/lib/x86_64-linux-gnu /usr/lib/aarch64-linux-gnu; do
    if [[ -e "${d}/libsqlite3.so" || -e "${d}/libsqlite3.a" ]]; then
      printf '%s' "${d}"
      return 0
    fi
  done
  return 1
}

# Copy one file or symlink if it exists. Never invoke mv with a glob.
sqlite3_copy_if() {
  local src="$1"
  local dest_dir="$2"
  [[ -e "${src}" ]] || return 0
  mkdir -p "${dest_dir}"
  cp -a "${src}" "${dest_dir}/" 2>/dev/null || true
}

sqlite3_install() {
  local heroku_python_dir="$1"
  local headers_only="${3:-}"
  local triplet libdir pc src f

  mkdir -p "${heroku_python_dir}/include" "${heroku_python_dir}/lib/pkgconfig" "${heroku_python_dir}/bin"

  triplet="$(sqlite3_gnu_triplet)"
  libdir="$(sqlite3_stack_libdir "${triplet}" || true)"
  if [[ -z "${libdir}" ]]; then
    return 1
  fi
  triplet="$(basename "${libdir}")"

  sqlite3_copy_if /usr/include/sqlite3.h "${heroku_python_dir}/include"
  sqlite3_copy_if /usr/include/sqlite3ext.h "${heroku_python_dir}/include"

  for f in "${libdir}"/libsqlite3.*; do
    [[ -e "${f}" ]] || continue
    sqlite3_copy_if "${f}" "${heroku_python_dir}/lib"
  done

  pc="${libdir}/pkgconfig/sqlite3.pc"
  if [[ -f "${pc}" ]]; then
    mkdir -p "${heroku_python_dir}/lib/pkgconfig"
    sed -e 's#prefix=/usr#prefix=/app/.heroku/python#' -e "s#/${triplet}##" "${pc}" \
      >"${heroku_python_dir}/lib/pkgconfig/sqlite3.pc" 2>/dev/null || true
  fi

  src="$(readlink -n "${libdir}/libsqlite3.so" 2>/dev/null || true)"
  if [[ -n "${src}" ]]; then
    ln -sfn "/usr/lib/${triplet}/${src}" "${heroku_python_dir}/lib/libsqlite3.so" 2>/dev/null || true
  elif [[ -e "/usr/lib/${triplet}/libsqlite3.so" ]]; then
    ln -sfn "/usr/lib/${triplet}/libsqlite3.so" "${heroku_python_dir}/lib/libsqlite3.so" 2>/dev/null || true
  fi

  if [[ -z "${headers_only}" && -x /usr/bin/sqlite3 ]]; then
    ln -sfn /usr/bin/sqlite3 "${heroku_python_dir}/bin/sqlite3" 2>/dev/null || true
  fi

  [[ -e "${heroku_python_dir}/include/sqlite3.h" ]] || [[ -e /usr/include/sqlite3.h ]]
}

buildpack_sqlite3_install() {
  if declare -F output::step >/dev/null; then
    output::step "Installing SQLite3"
  else
    echo "-----> Installing SQLite3"
  fi
  if sqlite3_install "${BUILD_DIR}/.heroku/python"; then
    :
if [[ -n "${BUILD_DIR:-}" ]]; then
  buildpack_sqlite3_install
fi

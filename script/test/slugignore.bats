#!/usr/bin/env bats

# EDGE-23: .slugignore is tenant input. Pruning must stay inside the build
# directory whatever the patterns say, and must still honour normal patterns.

load "helper"

setup() {
  source "${ROOT}/slugbuilder/builder/slugignore.sh"
  work="$(mktemp -d)"
  build_dir="${work}/build/app"
  outside="${work}/build/outside"
  mkdir -p "${build_dir}" "${outside}" "${work}/abs/path"
  echo keep > "${outside}/x"
  echo keep > "${work}/build/sibling"
  echo keep > "${work}/abs/path/file"
  echo keep > "${build_dir}/keep.txt"
}

teardown() {
  rm -rf "${work}"
}

slugignore() {
  printf '%s\n' "$@" > "${build_dir}/.slugignore"
}

@test "normal patterns, comments and blank lines still work" {
  mkdir -p "${build_dir}/docs/sub" "${build_dir}/lib" "${build_dir}/foo"
  echo a > "${build_dir}/README.md"
  echo a > "${build_dir}/CHANGES.md"
  echo a > "${build_dir}/docs/sub/a"
  echo a > "${build_dir}/lib/notes.md"
  echo a > "${build_dir}/lib/code.rb"
  echo a > "${build_dir}/foo/file"
  echo a > "${build_dir}/bar"
  slugignore '# comment asdf' '*.md' '  docs/  ' '' '     ' '/foo' 'lib/*.md' $'bar\r'
  run slugignore_prune "${build_dir}"
  assert_success
  [[ "${output}" == *"Deleting 6 files matching .slugignore patterns."* ]]
  [[ ! -e "${build_dir}/README.md" && ! -e "${build_dir}/CHANGES.md" ]]
  [[ ! -e "${build_dir}/docs" && ! -e "${build_dir}/foo" && ! -e "${build_dir}/bar" ]]
  [[ ! -e "${build_dir}/lib/notes.md" ]]
  [[ -f "${build_dir}/lib/code.rb" && -f "${build_dir}/keep.txt" && -f "${build_dir}/.slugignore" ]]
}

@test "parent-directory patterns leave files outside the build directory alone" {
  slugignore '../outside' '../outside/x' '../../build' '../sibling' './../sibling' \
    'docs/../../sibling' '..' '../*' '*/../../sibling' '.*' '.*/sibling' '.' './' '*/..'
  # bash before 5.2 lets '.*' match '.' and '..'; behave like that here too
  shopt -u globskipdots 2>/dev/null || true
  run slugignore_prune "${build_dir}"
  assert_success
  [[ -f "${outside}/x" && -f "${work}/build/sibling" ]]
  [[ -d "${build_dir}" && -f "${build_dir}/keep.txt" ]]
}

@test "absolute paths are relative to the build directory" {
  mkdir -p "${build_dir}/tmp"
  echo a > "${build_dir}/tmp/junk"
  slugignore "${work}/abs/path/file" "${outside}/x" '/tmp/junk' '//tmp'
  run slugignore_prune "${build_dir}"
  assert_success
  [[ -f "${work}/abs/path/file" && -f "${outside}/x" ]]
  [[ ! -e "${build_dir}/tmp" ]]
}

@test "symlinks cannot carry a pattern out of the build directory" {
  ln -s "${outside}" "${build_dir}/link"
  ln -s "${outside}/x" "${build_dir}/filelink"
  ln -s "${work}/build" "${build_dir}/up"
  slugignore 'link/*' 'link/' 'link/x' 'up/sibling' 'up/outside/*' 'filelink'
  run slugignore_prune "${build_dir}"
  assert_success
  [[ -f "${outside}/x" && -f "${work}/build/sibling" ]]
  # a matched symlink itself is removed, not what it points at
  [[ ! -L "${build_dir}/filelink" ]]
}

@test "a .slugignore symlink is not followed" {
  echo 'x' > "${outside}/x.list"
  echo a > "${build_dir}/x"
  ln -s "${outside}/x.list" "${build_dir}/.slugignore"
  run slugignore_prune "${build_dir}"
  assert_success
  [[ -f "${build_dir}/x" && -f "${outside}/x.list" ]]
}

@test "spaces, quotes, command substitution and leading dashes are literal" {
  echo a > "${build_dir}/my file.txt"
  echo a > "${build_dir}/my"
  echo a > "${build_dir}/file.txt"
  echo a > "${build_dir}/-rf"
  echo a > "${build_dir}/-e"
  echo a > "${build_dir}/\"quoted\""
  echo a > "${build_dir}/quoted"
  slugignore 'my file.txt' '-rf' '-e' '"quoted"' "\$(touch ${work}/pwned)" "\`touch ${work}/pwned2\`" \
    '--no-preserve-root' '-- *' '; rm -rf ..' '$HOME' '${build_dir}'
  run slugignore_prune "${build_dir}"
  assert_success
  [[ ! -e "${build_dir}/my file.txt" && ! -e "${build_dir}/-rf" && ! -e "${build_dir}/-e" ]]
  [[ ! -e "${build_dir}/\"quoted\"" ]]
  [[ -f "${build_dir}/my" && -f "${build_dir}/file.txt" && -f "${build_dir}/quoted" ]]
  [[ -f "${build_dir}/keep.txt" && -f "${outside}/x" ]]
  [[ ! -e "${work}/pwned" && ! -e "${work}/pwned2" ]]
}

@test "unsafe patterns are reported in the build output" {
  slugignore '../outside'
  run slugignore_prune "${build_dir}"
  assert_success
  [[ "${output}" == *"Skipping .slugignore pattern '../outside'"* ]]
  [[ "${output}" == *"Deleting 0 files"* ]]
}

@test "no .slugignore is a no-op" {
  run slugignore_prune "${build_dir}"
  assert_success
  [[ -f "${build_dir}/keep.txt" ]]
}

@test "runs as a script with the build directory argument" {
  echo a > "${build_dir}/gone.md"
  slugignore '*.md'
  run bash "${ROOT}/slugbuilder/builder/slugignore.sh" "${build_dir}"
  assert_success
  [[ ! -e "${build_dir}/gone.md" && -f "${build_dir}/keep.txt" ]]
}

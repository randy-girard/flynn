package builder_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildScriptAlwaysPassesEnvDirAndStack(t *testing.T) {
	src, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if strings.Contains(text, "${envdir}") {
		t.Fatal("build.sh still skips ENV_DIR when the app has no config vars; heroku-buildpack-python v281 then fails with \"3: unbound variable\"")
	}
	if !strings.Contains(text, `export STACK="${STACK:-heroku-24}"`) {
		t.Fatal("build.sh must default STACK to heroku-24 for the slugbuilder-24 image")
	}
	if !strings.Contains(text, "\"${env_dir}\"") {
		t.Fatal("compile must receive the env directory")
	}
	cmd := exec.Command("bash", "-n", "build.sh")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

// heroku-buildpack-python v281 bin/compile starts with `set -u` and ENV_DIR="${3}".
// The bundled multi buildpack forwards that argument unquoted, so a missing
// third argument never reaches the child.
func TestPythonV281CompileRequiresThirdArg(t *testing.T) {
	dir := t.TempDir()
	compile := filepath.Join(dir, "compile")
	script := `#!/usr/bin/env bash
set -euo pipefail
BUILD_DIR="${1}"
CACHE_DIR="${2}"
ENV_DIR="${3}"
: "${STACK:?Required env var STACK is not set}"
test -d "${ENV_DIR}"
`
	if err := os.WriteFile(compile, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := exec.Command("bash", compile, dir, dir)
	if err := missing.Run(); err == nil {
		t.Fatal("compile without ENV_DIR should fail the way python v281 does")
	}
	envDir := filepath.Join(dir, "env")
	if err := os.Mkdir(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := exec.Command("bash", compile, dir, dir, envDir)
	ok.Env = append(os.Environ(), "STACK=heroku-24")
	if out, err := ok.CombinedOutput(); err != nil {
		t.Fatalf("compile with ENV_DIR and STACK: %v\n%s", err, out)
	}
}

// BUILD-07: buildpack code runs as an unprivileged user in the same
// environment as build.sh. The cache URLs carry bearer tokens, so root must
// do the cache GET and PUT and remove the URLs from the environment before any
// buildpack runs.
func TestBuildCacheTokensHiddenFromBuildpacks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	cache := filepath.Join(dir, "cache")
	for _, d := range []string{src, cache} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "restored"), []byte("hit"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "curl.log")
	childEnv := filepath.Join(dir, "child.env")
	script := `set -eo pipefail
source ./build-cache.sh
curl() {
  echo "curl $*" >> "${LOG}"
  case " $* " in
    *" PUT "*) cat > /dev/null ;;
    *) tar --create --gzip --directory "${SRC}" . ;;
  esac
}
export BUILD_CACHE_URL="http://blobstore/app-cache.tgz?token=readtok&exp=1"
export BUILD_CACHE_PUT_URL="http://blobstore/app-cache.tgz?token=writetok&exp=1"
build_cache_restore "${CACHE}"
# stands in for run_unprivileged ${buildpack}/bin/compile
bash -c 'env' > "${CHILD_ENV}"
build_cache_save "${CACHE}"
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "LOG="+log, "SRC="+src, "CACHE="+cache, "CHILD_ENV="+childEnv, "FLYNN_CACHE_COMPRESS=gzip")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(cache, "restored")); err != nil || string(b) != "hit" {
		t.Fatalf("cache was not restored: %v %q", err, b)
	}
	env, err := os.ReadFile(childEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"BUILD_CACHE", "readtok", "writetok"} {
		if strings.Contains(string(env), leak) {
			t.Fatalf("buildpack environment still contains %q:\n%s", leak, env)
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one GET and one PUT, got:\n%s", calls)
	}
	if !strings.Contains(lines[0], "readtok") || strings.Contains(lines[0], "writetok") || strings.Contains(lines[0], " PUT ") {
		t.Fatalf("restore must GET with the read URL only: %s", lines[0])
	}
	if !strings.Contains(lines[1], "writetok") || strings.Contains(lines[1], "readtok") || !strings.Contains(lines[1], " PUT ") {
		t.Fatalf("save must PUT with the write URL only: %s", lines[1])
	}
}

func TestBuildScriptUsesCacheHelpers(t *testing.T) {
	src, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{"source /builder/build-cache.sh", "build_cache_restore", "build_cache_save"} {
		if !strings.Contains(text, want) {
			t.Fatalf("build.sh must use %q", want)
		}
	}
	if strings.Contains(text, `"${BUILD_CACHE_URL}"`) {
		t.Fatal("build.sh must not use BUILD_CACHE_URL directly; build-cache.sh strips it from the environment")
	}
	if strings.Index(text, "build_cache_restore") > strings.Index(text, "/bin/compile") {
		t.Fatal("cache restore (which unsets the tokens) must run before the buildpack compile")
	}
}

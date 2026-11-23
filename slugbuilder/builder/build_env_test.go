package builder_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// BUILD-08: the cluster SSH_CLIENT_KEY must not be a file or variable that the
// unprivileged buildpack user can read. Root stages it in a root-only file,
// loads it into an ssh-agent whose socket the build user can use, and deletes
// the file. Buildpack code can sign with the key but cannot read it.
func TestSSHKeyNotReadableByBuildUser(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent not installed")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	secrets := filepath.Join(dir, "secrets")
	for _, d := range []string{home, secrets} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	keyPath := filepath.Join(dir, "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	keyText, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	// unix socket paths are limited to ~104 bytes, t.TempDir() can exceed that
	agentDir, err := os.MkdirTemp("/tmp", "sshag")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(agentDir)
	childEnv := filepath.Join(dir, "child.env")
	staged := filepath.Join(dir, "staged.txt")
	script := `set -eo pipefail
source ./ssh-key.sh
ssh_client_key_stage
# root-only file exists while staged
ls -ln "${SSH_CLIENT_KEY_FILE}" | awk '{print $1, $3}' > "${STAGED}"
ssh_client_key_load
[[ ! -e "${SSH_CLIENT_KEY_FILE}" ]] || { echo "key file still on disk"; exit 1; }
# stands in for run_unprivileged ${buildpack}/bin/compile
bash -c 'env' > "${CHILD_ENV}"
ssh-add -l >> "${CHILD_ENV}"
ssh_client_key_cleanup
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"SSH_CLIENT_KEY="+string(keyText),
		"SSH_CLIENT_HOSTS=github.com ssh-ed25519 AAAA",
		"SSH_SECRETS_DIR="+secrets,
		"SSH_AGENT_DIR="+agentDir,
		"STAGED="+staged,
		"CHILD_ENV="+childEnv,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	st, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	want := "-rw------- " + itoa(os.Getuid())
	if strings.TrimSpace(string(st)) != want {
		t.Fatalf("staged key mode/owner = %q, want %q (root-owned, 0600)", st, want)
	}
	env, err := os.ReadFile(childEnv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "PRIVATE KEY") || strings.Contains(string(env), "SSH_CLIENT_KEY=") {
		t.Fatalf("buildpack environment still contains the key:\n%s", env)
	}
	if !strings.Contains(string(env), "SSH_AUTH_SOCK=") {
		t.Fatalf("buildpack must see an ssh-agent socket:\n%s", env)
	}
	if !strings.Contains(string(env), "ED25519") {
		t.Fatalf("ssh-agent did not hold the key:\n%s", env)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "id_rsa")); err == nil {
		t.Fatal("key must not be written to the build user's ~/.ssh/id_rsa")
	}
	if b, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); err == nil && len(b) != 0 {
		t.Fatal("known_hosts should be removed by cleanup")
	}
}

func TestBuildScriptUsesSSHKeyHelpers(t *testing.T) {
	src, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{"source /builder/ssh-key.sh", "ssh_client_key_stage", "ssh_client_key_load", "ssh_client_key_cleanup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("build.sh must use %q", want)
		}
	}
	if strings.Contains(text, "${HOME}/.ssh/id_rsa") {
		t.Fatal("build.sh must not write the SSH key into the build user's home")
	}
	if strings.Index(text, "ssh_client_key_load") > strings.Index(text, "/bin/compile") {
		t.Fatal("agent must be loaded before the buildpack compile")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// EDGE-23: .slugignore is tenant input; root must not apply it.
func TestBuildScriptPrunesSlugignoreUnprivileged(t *testing.T) {
	src, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if strings.Contains(text, "prune_slugignore") || strings.Contains(text, "rm -rf ${paths") {
		t.Fatal("build.sh still prunes .slugignore as root with unquoted patterns")
	}
	call := `run_unprivileged bash /builder/slugignore.sh "${build_dir}"`
	if !strings.Contains(text, call) {
		t.Fatalf("build.sh must prune with %q", call)
	}
	if !strings.Contains(text, `setuidgid "${USER}" "$@"`) {
		t.Fatal(`run_unprivileged must pass its arguments quoted ("$@")`)
	}
	if strings.Index(text, call) > strings.Index(text, "source /builder/buildpack-policy.sh") {
		t.Fatal("prune must stay before buildpack detection")
	}
	manifest, err := os.ReadFile(filepath.Join("..", "..", "builder", "manifest.json.template"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"slugbuilder/builder/slugignore.sh": "/builder/slugignore.sh"`) {
		t.Fatal("builder/manifest.json.template must copy slugignore.sh into the slugbuilder image")
	}
}

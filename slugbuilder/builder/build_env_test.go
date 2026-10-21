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

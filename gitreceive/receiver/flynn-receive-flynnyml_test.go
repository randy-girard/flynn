package main

import (
	"os"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/flynnyml"
)

func TestRejectFlynnYmlBeforeBuildpack(t *testing.T) {
	err := rejectFlynnYml(stackHeroku24, true)
	if err == nil || err.Error() != flynnyml.StackRequired {
		t.Fatalf("err %v", err)
	}
	if err := rejectFlynnYml(stackContainer, true); err != nil {
		t.Fatal(err)
	}
	if err := rejectFlynnYml(stackHeroku24, false); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("flynn-receive.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := functionBody(string(body), "run")
	rej := strings.Index(fn, "rejectFlynnYml(")
	pack := strings.Index(fn, "deployBuildpack(")
	if rej < 0 || pack < 0 || rej > pack {
		t.Fatal("heroku-24 must reject flynn.yml before the buildpack build")
	}
}

func TestFlynnYmlBuildEnvOmitsRuntimeAndDockerfile(t *testing.T) {
	env := dockerBuildJobEnv("secret-key", "artifact-id", "abc123", map[string]string{
		"DOCKERFILE":   "Dockerfile.prod",
		"DATABASE_URL": "postgres://db",
	}, "app-1")
	env["CONTROLLER_KEY"] = "secret-key"
	applyFlynnYmlBuildEnv(env, "1\nart\n.\nDockerfile\n-\n0\n")
	if env["FLYNN_BUILD_SPEC"] == "" {
		t.Fatal("missing spec")
	}
	for _, k := range []string{"DOCKERFILE", "IMAGE_ARTIFACT_ID", "DATABASE_URL", "CONTROLLER_KEY"} {
		if _, ok := env[k]; ok {
			t.Fatalf("%s leaked into the build job", k)
		}
	}
}

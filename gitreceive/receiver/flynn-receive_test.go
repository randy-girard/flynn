package main

import (
	"bytes"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestBuildJob(t *testing.T) {
	app := &ct.App{ID: "app-id", Name: "myapp"}
	prev := &ct.Release{ID: "rel-id"}
	env := map[string]string{"FOO": "bar"}
	job := buildJob(&ct.Artifact{ID: "art"}, app, prev, env, "dockerbuilder", "/builder/build.sh")

	if len(job.Config.Args) != 1 || job.Config.Args[0] != "/builder/build.sh" {
		t.Fatalf("unexpected args: %v", job.Config.Args)
	}
	if !job.Config.Stdin || !job.Config.DisableLog {
		t.Fatal("expected Stdin and DisableLog to be set")
	}
	if job.Config.Env["FOO"] != "bar" {
		t.Fatal("job env not passed through")
	}
	if job.Partition != "background" {
		t.Fatalf("expected background partition, got %q", job.Partition)
	}
	wantMeta := map[string]string{
		"flynn-controller.app":      "app-id",
		"flynn-controller.app_name": "myapp",
		"flynn-controller.release":  "rel-id",
		"flynn-controller.type":     "dockerbuilder",
	}
	for k, v := range wantMeta {
		if job.Metadata[k] != v {
			t.Fatalf("metadata[%q] = %q, want %q", k, job.Metadata[k], v)
		}
	}
	if job.Resources == nil {
		t.Fatal("expected default resources")
	}
}

func TestDockerBuildJobEnv(t *testing.T) {
	env := dockerBuildJobEnv("secret-key", "artifact-id", "abc123", nil)
	if env["CONTROLLER_KEY"] != "secret-key" {
		t.Fatalf("CONTROLLER_KEY = %q", env["CONTROLLER_KEY"])
	}
	if env["IMAGE_ARTIFACT_ID"] != "artifact-id" {
		t.Fatalf("IMAGE_ARTIFACT_ID = %q", env["IMAGE_ARTIFACT_ID"])
	}
	if env["SOURCE_VERSION"] != "abc123" {
		t.Fatalf("SOURCE_VERSION = %q", env["SOURCE_VERSION"])
	}
	if env["BUILDKITD_FLAGS"] != "--root=/tmp/buildkitd --oci-worker-snapshotter=native" {
		t.Fatalf("BUILDKITD_FLAGS = %q", env["BUILDKITD_FLAGS"])
	}
	if env["CI"] != "true" || env["BUILDKIT_PROGRESS"] != "plain" {
		t.Fatal("expected CI and BUILDKIT_PROGRESS to be set")
	}
	if _, ok := env["DOCKERFILE"]; ok {
		t.Fatal("DOCKERFILE should be absent when not in release env")
	}

	env = dockerBuildJobEnv("k", "a", "v", map[string]string{"DOCKERFILE": "Dockerfile.prod"})
	if env["DOCKERFILE"] != "Dockerfile.prod" {
		t.Fatalf("DOCKERFILE = %q, want Dockerfile.prod", env["DOCKERFILE"])
	}
}

func TestResolveStack(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{name: "default", env: nil, want: stackHeroku24},
		{name: "empty value", env: map[string]string{"FLYNN_STACK": ""}, want: stackHeroku24},
		{name: "heroku-24", env: map[string]string{"FLYNN_STACK": stackHeroku24}, want: stackHeroku24},
		{name: "container", env: map[string]string{"FLYNN_STACK": stackContainer}, want: stackContainer},
		{name: "unknown", env: map[string]string{"FLYNN_STACK": "bogus"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStack(tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.env["FLYNN_STACK"])
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if got != tc.want {
				t.Fatalf("resolveStack = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocalHostID(t *testing.T) {
	cases := map[string]string{
		"node1-0ae0f774-b30b-42ae-953f-c16cfecc559c": "node1",
		"host2-abc":    "host2",
		"":             "",
		"nodash":       "",
		"-leadingdash": "",
	}
	for jobID, want := range cases {
		t.Setenv("FLYNN_JOB_ID", jobID)
		if got := localHostID(); got != want {
			t.Fatalf("localHostID(%q) = %q, want %q", jobID, got, want)
		}
	}
}

// flushWriter should pass bytes through unchanged when wrapping a file.
func TestSyncStdoutPassthrough(t *testing.T) {
	// A non-*os.File writer is returned as-is (no flushing wrapper).
	var buf bytes.Buffer
	if got := syncStdout(&buf); got != &buf {
		t.Fatal("syncStdout should return non-file writers unchanged")
	}
}

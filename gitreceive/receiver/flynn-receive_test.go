package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"

	"github.com/flynn/flynn/controller/authorizer"
	"github.com/flynn/flynn/controller/authz"
	"github.com/flynn/flynn/controller/tokensigner"
	ct "github.com/flynn/flynn/controller/types"
	host "github.com/flynn/flynn/host/types"
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

// newBuildKeyPair returns a signer and the base64url PKIX public key that the
// authorizer expects, so tests can mint then verify a build token.
func newBuildKeyPair(t *testing.T) (*tokensigner.Signer, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %s", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %s", err)
	}
	return tokensigner.New(priv), base64.URLEncoding.EncodeToString(pub)
}

// TestMintBuildToken verifies a minted token is scoped to build:artifacts + the
// target app, and round-trips through the authorizer.
func TestMintBuildToken(t *testing.T) {
	signer, pubKey := newBuildKeyPair(t)
	app := &ct.App{ID: "app-1", Name: "myapp"}

	tokenStr, err := mintBuildToken(signer, app, defaultBuildTimeout)
	if err != nil {
		t.Fatalf("mint: %s", err)
	}

	pk, err := authorizer.ParseTokenKey(pubKey)
	if err != nil {
		t.Fatalf("parse token key: %s", err)
	}
	tok, err := authorizer.New(nil, nil, pk, time.Hour).AuthorizeToken(tokenStr)
	if err != nil {
		t.Fatalf("authorize minted token: %s", err)
	}
	if tok.HasClusterAdmin() {
		t.Fatal("minted build token must not have cluster admin")
	}
	if len(tok.Scopes) != 1 || tok.Scopes[0] != authz.ScopeBuildArtifacts {
		t.Fatalf("scopes = %v, want [%s]", tok.Scopes, authz.ScopeBuildArtifacts)
	}
	if len(tok.AppGrants) != 1 || tok.AppGrants[0].AppID != "app-1" {
		t.Fatalf("app grants = %v, want app-1", tok.AppGrants)
	}
	// The token must satisfy both build routes for its own app only.
	if !authz.HTTPAllowed(tok, "POST", "/artifacts") {
		t.Fatal("minted token should be allowed to POST /artifacts")
	}
	if !authz.TarreceiveAllowed(tok) {
		t.Fatal("minted token should be allowed to push to tarreceive")
	}
	if authz.HTTPAllowed(tok, "POST", "/apps/other/releases") {
		t.Fatal("minted token must not write to other apps")
	}
}

// TestApplyBuildCredentialWithSigner verifies that when a signer is configured
// the cluster key is removed from the job env and the token is delivered via a
// root-only secret mount instead.
func TestApplyBuildCredentialWithSigner(t *testing.T) {
	signer, _ := newBuildKeyPair(t)
	app := &ct.App{ID: "app-1", Name: "myapp"}
	job := &host.Job{Config: host.ContainerConfig{Env: map[string]string{
		"CONTROLLER_KEY": "cluster-god-key",
		"SLUG_IMAGE_ID":  "x",
	}}}

	if err := applyBuildCredential(signer, job, app, nil); err != nil {
		t.Fatalf("applyBuildCredential: %s", err)
	}
	if _, ok := job.Config.Env["CONTROLLER_KEY"]; ok {
		t.Fatal("CONTROLLER_KEY must be removed from build job env when minting")
	}
	if job.Config.Env["SLUG_IMAGE_ID"] != "x" {
		t.Fatal("unrelated env should be preserved")
	}
	if len(job.Config.Secrets) != 1 || job.Config.Secrets[0].Path != buildTokenPath {
		t.Fatalf("expected token secret at %s, got %v", buildTokenPath, job.Config.Secrets)
	}
	if len(job.Config.Secrets[0].Data) == 0 {
		t.Fatal("secret token data must not be empty")
	}
}

// TestApplyBuildCredentialNoSigner verifies back-compat: with no signing key the
// job is left untouched (legacy CONTROLLER_KEY env behavior).
func TestApplyBuildCredentialNoSigner(t *testing.T) {
	app := &ct.App{ID: "app-1", Name: "myapp"}
	job := &host.Job{Config: host.ContainerConfig{Env: map[string]string{
		"CONTROLLER_KEY": "cluster-god-key",
	}}}

	if err := applyBuildCredential(nil, job, app, nil); err != nil {
		t.Fatalf("applyBuildCredential(nil): %s", err)
	}
	if job.Config.Env["CONTROLLER_KEY"] != "cluster-god-key" {
		t.Fatal("legacy CONTROLLER_KEY must be preserved when no signer configured")
	}
	if len(job.Config.Secrets) != 0 {
		t.Fatal("no secret should be added when no signer configured")
	}
}

// TestResolveBuildTimeout verifies the operator override is honored, clamped to
// the 30m cluster cap, and falls back safely on invalid or missing values.
func TestResolveBuildTimeout(t *testing.T) {
	// Ensure no ambient process-env override leaks into the table cases.
	t.Setenv(buildTimeoutEnv, "")

	cases := []struct {
		name       string
		releaseEnv map[string]string
		want       time.Duration
	}{
		{"default_when_unset", nil, defaultBuildTimeout},
		{"empty_value", map[string]string{buildTimeoutEnv: ""}, defaultBuildTimeout},
		{"valid_override", map[string]string{buildTimeoutEnv: "25m"}, 25 * time.Minute},
		{"clamped_to_max", map[string]string{buildTimeoutEnv: "2h"}, maxBuildTimeout},
		{"exactly_max", map[string]string{buildTimeoutEnv: "30m"}, maxBuildTimeout},
		{"invalid_falls_back", map[string]string{buildTimeoutEnv: "notaduration"}, defaultBuildTimeout},
		{"zero_falls_back", map[string]string{buildTimeoutEnv: "0s"}, defaultBuildTimeout},
		{"negative_falls_back", map[string]string{buildTimeoutEnv: "-5m"}, defaultBuildTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBuildTimeout(tc.releaseEnv); got != tc.want {
				t.Fatalf("resolveBuildTimeout(%v) = %s, want %s", tc.releaseEnv, got, tc.want)
			}
		})
	}
}

// TestResolveBuildTimeoutProcessEnvFallback verifies a gitreceive app-level
// default (process env) is used when the built app's release env has no
// override, and is still capped at the cluster max.
func TestResolveBuildTimeoutProcessEnvFallback(t *testing.T) {
	t.Setenv(buildTimeoutEnv, "20m")
	if got := resolveBuildTimeout(nil); got != 20*time.Minute {
		t.Fatalf("process-env fallback = %s, want 20m", got)
	}
	// Release env takes precedence over process env.
	if got := resolveBuildTimeout(map[string]string{buildTimeoutEnv: "10m"}); got != 10*time.Minute {
		t.Fatalf("release env precedence = %s, want 10m", got)
	}
	// Process-env override is still clamped to the cap.
	t.Setenv(buildTimeoutEnv, "45m")
	if got := resolveBuildTimeout(nil); got != maxBuildTimeout {
		t.Fatalf("process-env clamp = %s, want %s", got, maxBuildTimeout)
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

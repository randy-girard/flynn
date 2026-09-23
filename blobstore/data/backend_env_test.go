package data

import (
	"os"
	"strings"
	"testing"
)

func TestBackendsFromEnvDefaultPostgres(t *testing.T) {
	def, backends, err := BackendsFromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	if def != "postgres" || backends["postgres"]["backend"] != "postgres" {
		t.Fatalf("default=%s backends=%v", def, backends)
	}
}

func TestBackendsFromEnvS3AndDefault(t *testing.T) {
	env := []string{
		`BACKEND_S3MAIN=backend=s3 region=us-east-1 bucket=flynnblobstore access_key_id=AKIAexample secret_access_key=s3cret`,
		`DEFAULT_BACKEND=s3main`,
		`BACKEND_S3MAIN_KEY=ignored-because-not-in-params-pattern`,
	}
	def, backends, err := BackendsFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if def != "s3main" {
		t.Fatalf("default %q", def)
	}
	info := backends["s3main"]
	if info["backend"] != "s3" || info["bucket"] != "flynnblobstore" || info["access_key_id"] != "AKIAexample" {
		t.Fatalf("%v", info)
	}
	if info["key"] != "ignored-because-not-in-params-pattern" {
		t.Fatalf("overlay key: %v", info)
	}
	redacted := RedactBackendInfo(info)
	if redacted["secret_access_key"] != "***" || redacted["access_key_id"] != "AKIAexample" {
		t.Fatalf("redact: %v", redacted)
	}
}

func TestEncodeBackendParamsStableOrder(t *testing.T) {
	got := EncodeBackendParams(map[string]string{
		"secret_access_key": "s",
		"backend":           "minio",
		"bucket":            "b",
		"endpoint":          "127.0.0.1:9000",
		"insecure":          "true",
		"access_key_id":     "k",
	})
	want := "backend=minio endpoint=127.0.0.1:9000 insecure=true bucket=b access_key_id=k secret_access_key=s"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestParseBackendInfoFromParamsAndEnv(t *testing.T) {
	info, err := parseBackendInfo([]string{
		"BACKEND_S3_BACKEND=s3",
		"BACKEND_S3_BUCKET=blobs",
		"UNRELATED=1",
	}, "s3", "backend=s3 region=us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if info["backend"] != "s3" || info["region"] != "us-east-1" || info["bucket"] != "blobs" {
		t.Fatalf("%v", info)
	}

	if _, err := parseBackendInfo(nil, "s3", "not-a-pair"); err == nil {
		t.Fatal("malformed kv must fail")
	}
}

func TestNewFileRepoFromEnvUnknownBackend(t *testing.T) {
	t.Setenv("DEFAULT_BACKEND", "not-a-backend")
	if _, err := NewFileRepoFromEnv(nil); err == nil || !strings.Contains(err.Error(), "unknown default backend") {
		t.Fatalf("got %v", err)
	}

	t.Setenv("DEFAULT_BACKEND", "")
	t.Setenv("BACKEND_EXTRA", "backend=does-not-exist")
	if _, err := NewFileRepoFromEnv(nil); err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("got %v", err)
	}

	// Ensure we did not leave a configured extra backend for later tests.
	_ = os.Unsetenv("BACKEND_EXTRA")
}

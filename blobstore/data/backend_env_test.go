package data

import (
	"os"
	"strings"
	"testing"
)

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

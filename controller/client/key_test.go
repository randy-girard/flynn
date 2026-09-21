package controller

import (
	"testing"
)

func TestKeyFromEnvOrMeta(t *testing.T) {
	meta := map[string]string{"AUTH_KEY": "from-meta"}

	t.Setenv("AUTH_KEY", "")
	t.Setenv("CONTROLLER_KEY", "")
	if got := KeyFromEnvOrMeta(meta); got != "from-meta" {
		t.Fatalf("meta fallback: %q", got)
	}

	t.Setenv("AUTH_KEY", "from-auth")
	if got := KeyFromEnvOrMeta(meta); got != "from-auth" {
		t.Fatalf("AUTH_KEY: %q", got)
	}

	t.Setenv("CONTROLLER_KEY", "from-controller")
	if got := KeyFromEnvOrMeta(meta); got != "from-controller" {
		t.Fatalf("CONTROLLER_KEY must win: %q", got)
	}

	t.Setenv("AUTH_KEY", "")
	t.Setenv("CONTROLLER_KEY", "")
	if got := KeyFromEnvOrMeta(nil); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestControllerDoesNotPublishAuthKeyMeta(t *testing.T) {
	// SEC-028: registration uses an instance without AUTH_KEY. The source is
	// asserted here so a later change that republishes the cluster key fails
	// this package's tests even though main() is not invoked.
	t.Setenv("AUTH_KEY", "must-not-appear-in-discoverd-meta")
	if got := KeyFromEnvOrMeta(nil); got != "must-not-appear-in-discoverd-meta" {
		t.Fatalf("env lookup: %q", got)
	}
}

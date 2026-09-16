package main

import (
	"testing"
	"time"
)

func TestGoBuildParallelFlag(t *testing.T) {
	t.Setenv("FLYNN_GO_BUILD_P", "")
	t.Setenv("GITHUB_ACTIONS", "")
	if got := goBuildParallelFlag(0); got != "" {
		t.Fatalf("empty default = %q", got)
	}
	if got := goBuildParallelFlag(3); got != "-p 3 " {
		t.Fatalf("layer p = %q", got)
	}

	t.Setenv("FLYNN_GO_BUILD_P", "2")
	if got := goBuildParallelFlag(0); got != "-p 2 " {
		t.Fatalf("env p = %q", got)
	}
	if got := goBuildParallelFlag(4); got != "-p 4 " {
		t.Fatalf("layer overrides env = %q", got)
	}

	t.Setenv("FLYNN_GO_BUILD_P", "")
	t.Setenv("GITHUB_ACTIONS", "true")
	if got := goBuildParallelFlag(0); got != "-p 2 " {
		t.Fatalf("gha default = %q", got)
	}
}

func TestImageBuildTimeout(t *testing.T) {
	t.Setenv("FLYNN_IMAGE_BUILD_TIMEOUT", "")
	t.Setenv("GITHUB_ACTIONS", "")
	if got := imageBuildTimeout(); got != 0 {
		t.Fatalf("local default = %s, want 0", got)
	}

	t.Setenv("GITHUB_ACTIONS", "true")
	if got := imageBuildTimeout(); got != 30*time.Minute {
		t.Fatalf("gha default = %s, want 30m", got)
	}

	t.Setenv("FLYNN_IMAGE_BUILD_TIMEOUT", "15m")
	if got := imageBuildTimeout(); got != 15*time.Minute {
		t.Fatalf("env timeout = %s, want 15m", got)
	}

	t.Setenv("FLYNN_IMAGE_BUILD_TIMEOUT", "0s")
	if got := imageBuildTimeout(); got != 0 {
		t.Fatalf("zero timeout = %s, want 0", got)
	}
}

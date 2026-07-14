package main

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestStackConstants(t *testing.T) {
	if stackHeroku24 != "heroku-24" || stackContainer != "container" {
		t.Fatalf("unexpected stack constants: %q %q", stackHeroku24, stackContainer)
	}
}

func TestDefaultStackFromRelease(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want string
	}{
		{nil, stackHeroku24},
		{map[string]string{}, stackHeroku24},
		{map[string]string{"FLYNN_STACK": "container"}, stackContainer},
	}
	for _, tc := range tests {
		stack := tc.env["FLYNN_STACK"]
		if stack == "" {
			stack = stackHeroku24
		}
		if stack != tc.want {
			t.Fatalf("env %#v => %q, want %q", tc.env, stack, tc.want)
		}
	}
}

func TestReleaseMetaForContainerStack(t *testing.T) {
	meta := map[string]string{"git.commit": "abc"}
	meta["git"] = "true"
	meta["slugrunner.stack"] = stackContainer
	if meta["slugrunner.stack"] != "container" {
		t.Fatalf("meta = %#v", meta)
	}
	_ = ct.Release{}
}

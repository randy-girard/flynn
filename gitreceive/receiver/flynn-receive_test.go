package main

import (
	"bytes"
	"testing"
)

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
		"host2-abc":  "host2",
		"":           "",
		"nodash":     "",
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

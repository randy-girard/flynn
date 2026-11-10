package data

import (
	"strings"
	"testing"
)

// A scheduler restart must see compiles that are already running. A
// pending-only list would let the next tick start another account's builds
// until the cluster cap is blown.
func TestInflightBuildListIncludesRunning(t *testing.T) {
	for _, state := range []string{"'pending'", "'blocked'", "'starting'", "'up'"} {
		if !strings.Contains(buildPendingListQuery, state) {
			t.Fatalf("in-flight build list must include %s", state)
		}
	}
	if strings.Contains(buildPendingListQuery, "state = 'pending'") {
		t.Fatal("pending-only build list hides running compiles from admission")
	}
}

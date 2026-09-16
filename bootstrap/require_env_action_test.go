package bootstrap

import (
	"strings"
	"testing"
)

func TestRequireEnv(t *testing.T) {
	t.Setenv("FLYNN_TEST_REQUIRE_A", "1")
	t.Setenv("FLYNN_TEST_REQUIRE_B", "")
	err := (&RequireEnv{Vars: []string{"FLYNN_TEST_REQUIRE_A", "FLYNN_TEST_REQUIRE_B", "FLYNN_TEST_REQUIRE_MISSING"}}).Run(&State{})
	if err == nil {
		t.Fatal("expected missing vars")
	}
	msg := err.Error()
	if !strings.Contains(msg, "FLYNN_TEST_REQUIRE_B") || !strings.Contains(msg, "FLYNN_TEST_REQUIRE_MISSING") {
		t.Fatalf("missing list: %s", msg)
	}
	if strings.Contains(msg, "FLYNN_TEST_REQUIRE_A") {
		t.Fatalf("set var listed as missing: %s", msg)
	}

	t.Setenv("FLYNN_TEST_REQUIRE_B", "set")
	t.Setenv("FLYNN_TEST_REQUIRE_MISSING", "set")
	if err := (&RequireEnv{Vars: []string{"FLYNN_TEST_REQUIRE_A", "FLYNN_TEST_REQUIRE_B", "FLYNN_TEST_REQUIRE_MISSING"}}).Run(&State{}); err != nil {
		t.Fatal(err)
	}
}

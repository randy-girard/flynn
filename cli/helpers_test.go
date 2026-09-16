package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNeedsFlynnLoginHint(t *testing.T) {
	if needsFlynnLoginHint(nil) {
		t.Fatal("nil")
	}
	if needsFlynnLoginHint(errors.New("no app found")) {
		t.Fatal("unrelated")
	}
	for _, msg := range []string{
		"invalid_grant",
		"invalid_token",
		"token_expired",
		"expired token",
		"id_token expired",
		"refresh token is invalid",
		"unauthorized_client",
		"access_denied",
		"not_before_claim",
		"INVALID_GRANT from oauth",
	} {
		if !needsFlynnLoginHint(errors.New(msg)) {
			t.Fatalf("want hint for %q", msg)
		}
	}
}

func TestHumanTimeAndListRec(t *testing.T) {
	if humanTime(nil) != "" {
		t.Fatal("nil time")
	}
	zero := time.Time{}
	if humanTime(&zero) != "" {
		t.Fatal("zero time")
	}
	ts := time.Now().UTC().Add(-2 * time.Minute)
	got := humanTime(&ts)
	if !strings.Contains(got, "ago") {
		t.Fatalf("humanTime=%q", got)
	}

	var buf strings.Builder
	listRec(&buf, "a", "b", "c")
	if got := buf.String(); got != "a\tb\tc\n" {
		t.Fatalf("listRec=%q", got)
	}
}

func TestApplyGlobalFlagsAndPositionalArgsNil(t *testing.T) {
	if err := applyGlobalFlags(nil); err != nil {
		t.Fatal(err)
	}
	cmd, args := positionalArgs(nil)
	if cmd != "" || args != nil {
		t.Fatalf("%q %v", cmd, args)
	}
	if helpFlag(nil) {
		t.Fatal("nil help")
	}
}

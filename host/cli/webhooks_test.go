package cli

import (
	"strings"
	"testing"
)

func TestParseHeaderFlags(t *testing.T) {
	got, err := parseHeaderFlags([]string{
		"X-Flynn-Webhook-Secret: s3cret",
		"X-Extra=value",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["X-Flynn-Webhook-Secret"] != "s3cret" || got["X-Extra"] != "value" {
		t.Fatalf("%v", got)
	}

	empty, err := parseHeaderFlags(nil)
	if err != nil || empty != nil {
		t.Fatalf("nil=%v err=%v", empty, err)
	}

	if _, err := parseHeaderFlags([]string{"nocolon"}); err == nil || !strings.Contains(err.Error(), "Name: value") {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := parseHeaderFlags([]string{": value"}); err == nil {
		t.Fatal("empty name")
	}
}

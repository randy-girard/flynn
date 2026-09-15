package name

import (
	"strings"
	"testing"
)

func TestGetIsDeterministicAndUnique(t *testing.T) {
	SetSeed([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	a := Get(1)
	b := Get(1)
	if a != b || a == "" || strings.Count(a, "-") != 2 {
		t.Fatalf("%q %q", a, b)
	}
	if Get(2) == "" {
		t.Fatal("empty name")
	}
}

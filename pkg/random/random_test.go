package random

import (
	"regexp"
	"testing"
)

func TestStringHexBase64AndUUID(t *testing.T) {
	if Math == nil {
		t.Fatal("Math RNG must be seeded from crypto/rand")
	}
	s := String(12)
	if len(s) != 12 {
		t.Fatalf("String len=%d", len(s))
	}
	h := Hex(8)
	if len(h) != 16 {
		t.Fatalf("Hex=%s", h)
	}
	b := Base64(16)
	if stringsContainsPadding(b) {
		t.Fatalf("Base64 should trim padding: %s", b)
	}
	id := UUID()
	if matched, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id); !matched {
		t.Fatalf("UUID=%s", id)
	}
	if UUID() == id {
		t.Fatal("UUIDs must not collide")
	}
	if len(Bytes(7)) != 7 {
		t.Fatal("Bytes length")
	}
}

func stringsContainsPadding(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return true
		}
	}
	return false
}

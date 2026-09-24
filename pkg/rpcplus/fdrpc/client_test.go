package fdrpc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDialMissingSocket(t *testing.T) {
	_, err := Dial(filepath.Join(os.TempDir(), "fdrpc-no-such.sock"))
	if err == nil {
		t.Fatal("expected error dialing a missing unix socket")
	}
}

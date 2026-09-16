package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCredentialTokenFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  ghp_example  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	tok, err := readCredentialToken(path)
	if err != nil || tok != "ghp_example" {
		t.Fatalf("%q %v", tok, err)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialToken(empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v", err)
	}

	if _, err := readCredentialToken(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file")
	}
}

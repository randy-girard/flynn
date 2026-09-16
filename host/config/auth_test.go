package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAuthKeyMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	key, err := LoadAuthKey(missing)
	if err != nil || key != "" {
		t.Fatalf("missing file: key=%q err=%v", key, err)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"args":["--foo"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	key, err = LoadAuthKey(empty)
	if err != nil || key != "" {
		t.Fatalf("empty env: key=%q err=%v", key, err)
	}

	if _, err := LoadAuthKey(filepath.Join(dir, "bad.json")); err == nil {
		// write invalid JSON
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthKey(filepath.Join(dir, "bad.json")); err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestSetAuthKeyPersistsAndChmods(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	if err := SetAuthKey(path, "first-secret"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("perm=%o", st.Mode().Perm())
	}
	key, err := LoadAuthKey(path)
	if err != nil || key != "first-secret" {
		t.Fatalf("load: %q %v", key, err)
	}

	// Preserve existing args when rotating the key.
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Args = []string{"--external-ip", "10.0.0.1"}
	if err := c.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	if err := SetAuthKey(path, "rotated"); err != nil {
		t.Fatal(err)
	}
	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Env["FLYNN_HOST_AUTH_KEY"] != "rotated" {
		t.Fatalf("env=%v", c.Env)
	}
	if len(c.Args) != 2 || c.Args[1] != "10.0.0.1" {
		t.Fatalf("args=%v", c.Args)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "first-secret") {
		t.Fatal("old key must not remain on disk")
	}
}

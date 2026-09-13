package iptables

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIPSetBinMissingFromPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ipsetBin()
	if err == nil {
		t.Fatal("expected error when ipset is not on PATH")
	}
	if !strings.Contains(err.Error(), "ipset not found") {
		t.Fatalf("got %v", err)
	}
}

func TestEnsureSetsFailsClosedWithoutIPSet(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := EnsureSets("flynn-net-user")
	if err == nil {
		t.Fatal("EnsureSets must fail when ipset is missing (isolation cannot fail open)")
	}
	if !strings.Contains(err.Error(), "ipset not found") {
		t.Fatalf("got %v", err)
	}
}

func TestEnableJobIsolationPropagatesEnsureSetsError(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("iptables.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	iso := body
	if i := strings.Index(body, "func EnableJobIsolation"); i >= 0 {
		iso = body[i:]
		if j := strings.Index(iso[1:], "\nfunc "); j >= 0 {
			iso = iso[:j+1]
		}
	}
	if !strings.Contains(iso, "if err := EnsureSets(") {
		t.Fatal("EnableJobIsolation must call EnsureSets")
	}
	if !strings.Contains(iso, "return err") {
		t.Fatal("EnableJobIsolation must return EnsureSets errors (fail closed)")
	}
}

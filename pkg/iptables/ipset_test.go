package iptables

import (
	"net"
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

func TestUnionIPsDedupesAndSkipsNil(t *testing.T) {
	a := net.ParseIP("100.64.0.1")
	b := net.ParseIP("100.64.0.2")
	got := UnionIPs([]net.IP{a, a, nil}, []net.IP{b, a})
	if len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if AddSetIP("flynn-net-user", nil) != nil {
		t.Fatal("nil IP add is a no-op")
	}
	if DelSetIP("flynn-net-user", nil) != nil {
		t.Fatal("nil IP del is a no-op")
	}
	if bytesPreview(nil) != "" {
		t.Fatal("empty preview")
	}
	long := strings.Repeat("x", 250)
	if got := bytesPreview([]byte("  " + long)); len(got) != 200 {
		t.Fatalf("preview len=%d", len(got))
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

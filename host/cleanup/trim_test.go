package cleanup

import (
	"errors"
	"strings"
	"testing"
)

func TestTrimZpoolSkipsEmptyPool(t *testing.T) {
	orig := trimZpoolCmd
	t.Cleanup(func() { trimZpoolCmd = orig })
	called := false
	trimZpoolCmd = func(string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
	}
	if err := TrimZpool("  "); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("empty pool must not invoke zpool")
	}
}

func TestTrimZpoolWaitsOnSuccess(t *testing.T) {
	orig := trimZpoolCmd
	t.Cleanup(func() { trimZpoolCmd = orig })
	var got string
	trimZpoolCmd = func(pool string) ([]byte, error) {
		got = pool
		return []byte("trim complete"), nil
	}
	if err := TrimZpool("flynn-default"); err != nil {
		t.Fatal(err)
	}
	if got != "flynn-default" {
		t.Fatalf("pool %q", got)
	}
}

func TestTrimZpoolSkipsUnsupportedAndMissing(t *testing.T) {
	orig := trimZpoolCmd
	t.Cleanup(func() { trimZpoolCmd = orig })
	cases := []string{
		"cannot trim 'flynn-default': trim operations are not supported by this device",
		"cannot open 'flynn-default': no such pool",
		"dataset does not exist",
	}
	for _, out := range cases {
		trimZpoolCmd = func(string) ([]byte, error) {
			return []byte(out), errors.New("exit status 1")
		}
		if err := TrimZpool("flynn-default"); err != nil {
			t.Fatalf("%q: %v", out, err)
		}
	}
	trimZpoolCmd = func(string) ([]byte, error) {
		return nil, errors.New(`exec: "zpool": executable file not found in $PATH`)
	}
	if err := TrimZpool("flynn-default"); err != nil {
		t.Fatal(err)
	}
}

func TestTrimZpoolReportsRealFailure(t *testing.T) {
	orig := trimZpoolCmd
	t.Cleanup(func() { trimZpoolCmd = orig })
	trimZpoolCmd = func(string) ([]byte, error) {
		return []byte("I/O error"), errors.New("exit status 1")
	}
	err := TrimZpool("flynn-default")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "I/O error") {
		t.Fatalf("got %v", err)
	}
}

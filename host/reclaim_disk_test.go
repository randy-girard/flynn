package main

import (
	"errors"
	"strings"
	"testing"

	zfsVolume "github.com/randy-girard/flynn/host/volume/zfs"
)

func TestReclaimDiskTrimsDefaultPool(t *testing.T) {
	orig := trimPool
	t.Cleanup(func() { trimPool = orig })
	var got string
	trimPool = func(pool string) error {
		got = pool
		return nil
	}
	h := &Host{}
	if err := h.ReclaimDisk(); err != nil {
		t.Fatal(err)
	}
	if got != zfsVolume.DefaultDatasetName {
		t.Fatalf("pool %q", got)
	}
}

func TestReclaimDiskTrimsConfiguredPool(t *testing.T) {
	orig := trimPool
	t.Cleanup(func() { trimPool = orig })
	var got string
	trimPool = func(pool string) error {
		got = pool
		return nil
	}
	h := &Host{zpoolName: "custom-pool"}
	if err := h.ReclaimDisk(); err != nil {
		t.Fatal(err)
	}
	if got != "custom-pool" {
		t.Fatalf("pool %q", got)
	}
}

func TestReclaimDiskReturnsTrimError(t *testing.T) {
	orig := trimPool
	t.Cleanup(func() { trimPool = orig })
	trimPool = func(string) error {
		return errors.New("I/O error")
	}
	err := (&Host{}).ReclaimDisk()
	if err == nil || !strings.Contains(err.Error(), "I/O error") {
		t.Fatalf("got %v", err)
	}
}

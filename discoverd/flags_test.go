package main

import (
	"reflect"
	"testing"
)

func TestTrimSpaceSlice_TrailingCommaAndDuplicates(t *testing.T) {
	got := TrimSpaceSlice([]string{"192.168.57.20:1111", "", "192.168.57.20:1111", " 192.168.57.21:1111 "})
	want := []string{"192.168.57.20:1111", "192.168.57.21:1111"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseFlags_TrailingCommaPeerIsSingleNode(t *testing.T) {
	m := NewMain()
	opt, err := m.ParseFlags(
		"-data-dir", "/tmp/data/dir",
		"-host", "192.168.57.20",
		"-addr", ":1111",
		"-peers", "192.168.57.20:1111,",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opt.Peers, []string{"192.168.57.20:1111"}) {
		t.Fatalf("unexpected peers: %#v", opt.Peers)
	}
	if len(opt.Peers) > 1 {
		t.Fatal("trailing comma must not disable single-node raft")
	}
}

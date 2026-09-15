package xlog

import "testing"

func TestCompare(t *testing.T) {
	x := XLog{}
	if x.Zero() != "" {
		t.Fatal(x.Zero())
	}
	cmp, err := x.Compare("10", "10")
	if err != nil || cmp != 0 {
		t.Fatalf("%d %v", cmp, err)
	}
	cmp, err = x.Compare("20", "10")
	if err != nil || cmp != 1 {
		t.Fatalf("%d %v", cmp, err)
	}
	cmp, err = x.Compare("", "5")
	if err != nil || cmp != -1 {
		t.Fatalf("empty is zero: %d %v", cmp, err)
	}
	if _, err := x.Compare("nope", "1"); err == nil {
		t.Fatal("malformed")
	}
}

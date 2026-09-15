package pgxlog

import "testing"

func TestCompareAndIncrement(t *testing.T) {
	p := PgXLog{}
	if p.Zero() != Zero {
		t.Fatal(p.Zero())
	}
	cmp, err := p.Compare("0/17BB660", "0/17BB660")
	if err != nil || cmp != 0 {
		t.Fatalf("%d %v", cmp, err)
	}
	cmp, err = p.Compare("1/0", "0/FFFFFFFF")
	if err != nil || cmp != 1 {
		t.Fatalf("newer filepart: %d %v", cmp, err)
	}
	cmp, err = p.Compare("0/10", "0/20")
	if err != nil || cmp != -1 {
		t.Fatalf("older offset: %d %v", cmp, err)
	}
	if _, err := p.Compare("bad", "0/1"); err == nil {
		t.Fatal("malformed")
	}

	got, err := p.Increment("A/00000010", 16)
	if err != nil || got != "A/00000020" {
		t.Fatalf("%q %v", got, err)
	}
}

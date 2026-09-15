package mdbxlog

import "testing"

func TestCompare(t *testing.T) {
	m := MDBXLog{}
	if m.Zero() != "" {
		t.Fatal(m.Zero())
	}
	cmp, err := m.Compare("", "")
	if err != nil || cmp != 0 {
		t.Fatalf("%d %v", cmp, err)
	}
	cmp, err = m.Compare("1-2-100", "1-2-50")
	if err != nil || cmp != 1 {
		t.Fatalf("%d %v", cmp, err)
	}
	cmp, err = m.Compare("1-2-1", "1-2-2")
	if err != nil || cmp != -1 {
		t.Fatalf("%d %v", cmp, err)
	}
	if _, err := m.Compare("not-an-xlog", "1-2-3"); err == nil {
		t.Fatal("malformed")
	}
}

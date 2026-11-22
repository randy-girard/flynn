package uploadlimit

import (
	"bytes"
	"io"
	"io/ioutil"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in   string
		def  int64
		want int64
		err  bool
	}{
		{"", 42, 42, false},
		{"  ", 42, 42, false},
		{"0", 42, 0, false},
		{"off", 42, 0, false},
		{"OFF", 42, 0, false},
		{"1024", 0, 1024, false},
		{"1k", 0, 1024, false},
		{"512m", 0, 512 << 20, false},
		{"32G", 0, 32 << 30, false},
		{"32gb", 0, 32 << 30, false},
		{"1t", 0, 1 << 40, false},
		{"lots", 0, 0, true},
		{"-5", 0, 0, true},
	} {
		got, err := Parse(c.in, c.def)
		if c.err {
			if err == nil {
				t.Errorf("Parse(%q) = %d, want an error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Parse(%q, %d) = %d, %v; want %d", c.in, c.def, got, err, c.want)
		}
	}
}

func TestReaderAllowsExactLimit(t *testing.T) {
	r := NewReader(strings.NewReader("12345"), 5)
	got, err := ioutil.ReadAll(r)
	if err != nil || string(got) != "12345" || r.Exceeded {
		t.Fatalf("ReadAll = %q, %v, exceeded=%v", got, err, r.Exceeded)
	}
}

func TestReaderStopsPastLimit(t *testing.T) {
	r := NewReader(strings.NewReader("123456789"), 5)
	var buf bytes.Buffer
	_, err := io.Copy(&buf, r)
	if err != ErrTooLarge || !r.Exceeded {
		t.Fatalf("err = %v exceeded=%v, want ErrTooLarge", err, r.Exceeded)
	}
	if buf.Len() > 5 {
		t.Fatalf("passed %d bytes, limit is 5", buf.Len())
	}
	if _, err := r.Read(make([]byte, 1)); err != ErrTooLarge {
		t.Fatalf("read after exceeding = %v", err)
	}
}

func TestReaderNoLimit(t *testing.T) {
	r := NewReader(strings.NewReader(strings.Repeat("x", 10000)), 0)
	got, err := ioutil.ReadAll(r)
	if err != nil || len(got) != 10000 || r.N() != 10000 {
		t.Fatalf("ReadAll = %d bytes, %v", len(got), err)
	}
}

func TestBudgetReaderZero(t *testing.T) {
	r := NewBudgetReader(strings.NewReader(""), 0)
	if got, err := ioutil.ReadAll(r); err != nil || len(got) != 0 {
		t.Fatalf("empty body = %q, %v", got, err)
	}
	r = NewBudgetReader(strings.NewReader("x"), 0)
	if _, err := ioutil.ReadAll(r); err != ErrTooLarge || !r.Exceeded {
		t.Fatalf("one byte over a zero budget = %v", err)
	}
	r = NewBudgetReader(strings.NewReader("x"), -3)
	if _, err := ioutil.ReadAll(r); err != ErrTooLarge {
		t.Fatalf("negative budget = %v", err)
	}
}

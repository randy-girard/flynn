package rfc6587

import (
	"bufio"
	"strings"
	"testing"
)

func callSplit(t *testing.T, f bufio.SplitFunc, data string, atEOF bool) (advance int, token []byte, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("split(%q) panicked: %v", data, r)
		}
	}()
	return f([]byte(data), atEOF)
}

func TestSplitRejectsNegativeLength(t *testing.T) {
	for _, f := range []struct {
		name string
		fn   bufio.SplitFunc
	}{{"Split", Split}, {"SplitWithNewlines", SplitWithNewlines}} {
		for _, data := range []string{"-5 x", "-1 x", "-3 abcdef", "-9999 abc", "0 ", "0 x", "-0 x"} {
			advance, token, err := callSplit(t, f.fn, data, false)
			if err == nil {
				t.Errorf("%s(%q): expected an error, got advance=%d token=%q", f.name, data, advance, token)
			}
		}
	}
}

func TestSplitValidFrames(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("5 hello3 abc"))
	sc.Split(Split)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "hello" || got[1] != "abc" {
		t.Fatalf("got %q", got)
	}

	sc = bufio.NewScanner(strings.NewReader("5 hello\n3 abc\n"))
	sc.Split(SplitWithNewlines)
	got = nil
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "hello\n" || got[1] != "abc\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitPartialFrameRequestsMore(t *testing.T) {
	advance, token, err := callSplit(t, Split, "10 abc", false)
	if advance != 0 || token != nil || err != nil {
		t.Fatalf("got advance=%d token=%q err=%v", advance, token, err)
	}
}

func TestSplitRejectsOversizedLength(t *testing.T) {
	if _, _, err := callSplit(t, Split, "11001 x", false); err == nil {
		t.Fatal("expected an error")
	}
}

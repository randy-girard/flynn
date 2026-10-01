package logmux

import "testing"

func TestLineWriterSplitsCRAndLF(t *testing.T) {
	var got []string
	w := &LineWriter{WriteLine: func(s string) { got = append(got, s) }}
	if _, err := w.Write([]byte("echo hello\r\nhello\r\n$ ")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "echo hello" || got[1] != "hello" {
		t.Fatalf("got %#v", got)
	}
	w.Flush()
	if len(got) != 3 || got[2] != "$ " {
		t.Fatalf("flush %#v", got)
	}
}

func TestLineWriterKeepsCommandOnBareCR(t *testing.T) {
	var got []string
	w := &LineWriter{WriteLine: func(s string) { got = append(got, s) }}
	_, _ = w.Write([]byte("echo hello\r"))
	if len(got) != 1 || got[0] != "echo hello" {
		t.Fatalf("bare CR must keep the typed command, got %#v", got)
	}
}

package joboutput

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompressRoundTrip(t *testing.T) {
	plain := bytes.Repeat([]byte("-----> Building app\n"), 200)
	body, err := Compress(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Fatalf("body = %x, want gzip", body)
	}
	if len(body) >= len(plain) {
		t.Fatalf("compressed %d bytes is not smaller than %d", len(body), len(plain))
	}
	got, err := Decompress(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip len %d, want %d", len(got), len(plain))
	}
}

func TestTailKeepsTheEndOnALine(t *testing.T) {
	const max = 32
	head := strings.Repeat("a", 100)
	s := head + "\n-----> last line\n"
	got := Tail(s, max)
	if !strings.HasSuffix(got, "-----> last line\n") {
		t.Fatalf("got %q", got)
	}
	if len(got) > max {
		t.Fatalf("len %d > %d", len(got), max)
	}
	if strings.Contains(got, "aaa") && !strings.HasPrefix(got, "----->") {
		// A partial head line may remain only when it is its own line.
		if !strings.Contains(got, "\n") {
			t.Fatalf("tail starts mid-line: %q", got)
		}
	}
	if Tail(s, 0) != "" {
		t.Fatal("non-positive max must be empty")
	}
	if Tail("short\n", max) != "short\n" {
		t.Fatal("short text must be kept whole")
	}
}

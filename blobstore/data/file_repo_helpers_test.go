package data

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/blobstore/backend"
)

func TestGetBackendAndDefault(t *testing.T) {
	r := NewFileRepo(nil, []backend.Backend{backend.Postgres}, "postgres")
	b, err := r.getBackend("postgres")
	if err != nil || b == nil || b.Name() != "postgres" {
		t.Fatalf("%v %v", b, err)
	}
	if r.DefaultBackend() != b {
		t.Fatal("default")
	}
	if _, err := r.getBackend("s3"); err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("got %v", err)
	}
}

func TestSizeReaderCountsBytes(t *testing.T) {
	sr := newSizeReader(bytes.NewReader([]byte("abcd")))
	got, err := io.ReadAll(sr)
	if err != nil || string(got) != "abcd" || sr.Size() != 4 {
		t.Fatalf("%q size=%d err=%v", got, sr.Size(), err)
	}
}

func TestFakeSizeSeekerReportsLength(t *testing.T) {
	f := fakeSizeSeekerFileStream{size: 42}
	n, err := f.Seek(0, io.SeekEnd)
	if err != nil || n != 42 {
		t.Fatalf("seek end %d %v", n, err)
	}
	n, err = f.Seek(0, io.SeekStart)
	if err != nil || n != 0 {
		t.Fatalf("seek start %d %v", n, err)
	}
	buf := make([]byte, 1)
	if _, err := f.Read(buf); err != io.EOF {
		t.Fatalf("read %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

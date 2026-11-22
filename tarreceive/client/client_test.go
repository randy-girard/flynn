package client

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// EDGE-08: a layer read from a file is sent with its length, so tarreceive
// can refuse an oversize image before the upload starts. A stream of unknown
// length is still sent chunked.
func TestCreateLayerSendsFileLength(t *testing.T) {
	var length int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		length = r.ContentLength
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "key")

	f, err := os.CreateTemp(t.TempDir(), "layer")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Repeat("l", 3000)); err != nil {
		t.Fatal(err)
	}
	// the caller hashes the file first, then rewinds part of the way
	if _, err := f.Seek(1000, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateFlatLayer("abc", f); err != nil {
		t.Fatal(err)
	}
	if length != 2000 {
		t.Fatalf("Content-Length = %d, want the 2000 bytes left in the file", length)
	}

	if _, err := c.CreateLayer("abc", strings.NewReader("xyz")); err != nil {
		t.Fatal(err)
	}
	if length != 3 {
		t.Fatalf("Content-Length of a strings.Reader = %d, want 3", length)
	}
	if _, err := c.CreateLayer("abc", onlyReader{strings.NewReader("xyz")}); err != nil {
		t.Fatal(err)
	}
	if length != -1 {
		t.Fatalf("Content-Length of a stream = %d, want -1 (chunked)", length)
	}
}

type onlyReader struct{ r *strings.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

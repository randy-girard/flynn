package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
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

// EDGE-35: a client for an app names it on every layer and artifact request,
// so tarreceive stores them under that app's blobstore prefix.
func TestClientNamesApp(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "key")
	c.App = "app-1"
	if _, err := c.GetLayer("abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateFlatLayer("abc", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateLayer("abc", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateArtifact(&ct.ImageManifest{}); err != nil {
		t.Fatal(err)
	}
	c.App = ""
	if _, err := c.GetLayer("abc"); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /layer/abc?app=app-1", "POST /layer/abc?app=app-1", "POST /layer/abc?app=app-1", "POST /artifact?app=app-1", "GET /layer/abc"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

// EDGE-36: tarreceive answers a retryable 503 when it is already receiving
// MAX_CONCURRENT_UPLOADS layers or its temp disk is reserved by layers in
// progress. A layer read from a file is sent again after a pause; a stream,
// which cannot be rewound, and other errors are not retried.
func TestCreateLayerRetriesBusyTarreceive(t *testing.T) {
	var bodies []string
	busy, plain := 2, 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if plain > 0 {
			plain--
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if busy > 0 {
			busy--
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"code":"service_unavailable","message":"busy","retry":true}`))
			return
		}
		w.Write([]byte(`{"id":"abc"}`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	c := NewClient(srv.URL, "key")
	var waits []time.Duration
	c.retrySleep = func(d time.Duration) { waits = append(waits, d) }

	f, err := os.CreateTemp(t.TempDir(), "layer")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString("layer-data")
	f.Seek(0, io.SeekStart)
	layer, err := c.CreateFlatLayer("abc", f)
	if err != nil {
		t.Fatalf("busy tarreceive: %v", err)
	}
	if layer.ID != "abc" || len(bodies) != 3 || len(waits) != 2 {
		t.Fatalf("layer %+v after %d requests and %d waits", layer, len(bodies), len(waits))
	}
	for i, b := range bodies {
		if b != "layer-data" {
			t.Fatalf("request %d sent %q, want the whole file again", i, b)
		}
	}
	if waits[0] != 30*time.Second {
		t.Fatalf("waited %s, want the Retry-After", waits[0])
	}

	// the router answers a plain 503 when tarreceive does not read the
	// body for a few seconds (it is waiting for a slot); a file is sent
	// again then too
	plain, busy, bodies, waits = 1, 0, nil, nil
	f.Seek(0, io.SeekStart)
	if _, err := c.CreateFlatLayer("abc", f); err != nil {
		t.Fatalf("plain 503 from the router: %v", err)
	}
	if len(bodies) != 2 || bodies[1] != "layer-data" || len(waits) != 1 {
		t.Fatalf("plain 503: %d requests %q, %d waits", len(bodies), bodies, len(waits))
	}

	// a stream is not retried
	busy, bodies, waits = 1, nil, nil
	if _, err := c.CreateLayer("abc", onlyReader{strings.NewReader("x")}); err == nil {
		t.Fatal("a 503 for a stream was retried or ignored")
	}
	if len(bodies) != 1 {
		t.Fatalf("stream sent %d times", len(bodies))
	}

	// the retries stop
	busy, bodies, waits = 100, nil, nil
	f.Seek(0, io.SeekStart)
	if _, err := c.CreateFlatLayer("abc", f); err == nil {
		t.Fatal("a tarreceive that stays busy was retried forever")
	}
	if len(bodies) != maxBusyRetries+1 {
		t.Fatalf("sent %d times, want %d", len(bodies), maxBusyRetries+1)
	}
}

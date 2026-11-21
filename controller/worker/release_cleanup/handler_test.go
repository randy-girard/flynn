package release_cleanup

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlobClientHasTimeout(t *testing.T) {
	if blobClient == nil || blobClient.Timeout <= 0 {
		t.Fatal("release cleanup blobstore client has no timeout")
	}
}

// TestDeleteFileStalledBlobstoreFails: a blobstore that never answers must
// fail the attempt instead of blocking the worker.
func TestDeleteFileStalledBlobstoreFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-req.Context().Done():
		}
	}))
	defer srv.Close()
	orig := blobClient
	blobClient = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { blobClient = orig }()

	done := make(chan error, 1)
	go func() { done <- deleteFile(srv.URL + "/slug.tgz") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deleteFile still blocked on a stalled blobstore")
	}
}

func TestDeleteFileAcceptsMissing(t *testing.T) {
	for _, code := range []int{200, 202, 404} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(code)
		}))
		if err := deleteFile(srv.URL + "/slug.tgz"); err != nil {
			t.Errorf("status %d: %v", code, err)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if err := deleteFile(srv.URL + "/slug.tgz"); err == nil {
		t.Error("expected an error for status 500")
	}
}

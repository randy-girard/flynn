package httpclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRawReqAcceptsNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	if _, err := c.RawReq("GET", "/", nil, nil, nil); err != nil {
		t.Fatalf("expected 204 with nil out to succeed: %v", err)
	}
	if _, err := c.RawReq("POST", "/", nil, nil, nil); err != nil {
		t.Fatalf("expected 204 POST with nil out to succeed: %v", err)
	}
}

func TestRawReqNoContentWithBodyStillErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	var out map[string]string
	if _, err := c.RawReq("GET", "/", nil, nil, &out); err == nil {
		t.Fatal("expected error decoding 204 into out")
	}
}

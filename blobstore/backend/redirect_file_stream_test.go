package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedirectFileStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		io.WriteString(w, "layer-bytes")
	}))
	defer srv.Close()

	s := newRedirectFileStream(srv.URL + "/ok")
	if s.(Redirector).RedirectURL() != srv.URL+"/ok" {
		t.Fatal("redirect url")
	}
	if _, err := s.Seek(0, 0); err == nil {
		t.Fatal("seek must fail")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(s)
	if err != nil || string(got) != "layer-bytes" {
		t.Fatalf("%q %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	bad := newRedirectFileStream(srv.URL + "/missing")
	if _, err := io.ReadAll(bad); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("status: %v", err)
	}
}

package httpclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/httphelper"
)

// A JSON error body that is not a JSONError used to decode into an empty
// one, and the caller only saw ": " (EDGE-38). The status, URL and body must
// reach the error instead.
func TestRawReqNonJSONErrorBodyReportsStatus(t *testing.T) {
	for _, body := range []string{`{"error":"no backend"}`, `{}`, `[]`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(body))
		}))
		c := &Client{URL: srv.URL, HTTP: srv.Client()}
		err := c.Put("/apps/a/formations/r", map[string]string{}, &map[string]string{})
		srv.Close()
		if err == nil {
			t.Fatalf("body %s: want an error", body)
		}
		msg := err.Error()
		if strings.TrimSpace(strings.Trim(msg, ":")) == "" || msg == ": " {
			t.Fatalf("body %s: empty error %q", body, msg)
		}
		for _, want := range []string{"503", "/apps/a/formations/r", "PUT"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("body %s: error %q does not mention %s", body, msg, want)
			}
		}
		if body != `{}` && body != `[]` && !strings.Contains(msg, "no backend") {
			t.Fatalf("body %s: error %q leaves out the response body", body, msg)
		}
		var je httphelper.JSONError
		if errors.As(err, &je) {
			t.Fatalf("body %s: got JSONError %+v, want a status error", body, je)
		}
	}
}

// A 404 whose JSON body is not a JSONError is still ErrNotFound.
func TestRawReqNonJSONError404IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
	}))
	defer srv.Close()
	notFound := errors.New("missing")
	c := &Client{URL: srv.URL, HTTP: srv.Client(), ErrNotFound: notFound}
	if err := c.Get("/x", nil); err != notFound {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

// A real JSONError is still returned as one.
func TestRawReqJSONErrorIsKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httphelper.ValidationError(w, "processes", "is bad")
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	err := c.Get("/x", nil)
	je, ok := err.(httphelper.JSONError)
	if !ok || je.Code != httphelper.ValidationErrorCode {
		t.Fatalf("err=%#v, want a validation JSONError", err)
	}
}

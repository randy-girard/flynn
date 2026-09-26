package resource

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func TestProvisionWaitsPastTheRetryHeaderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Longer than RetryClient's 10s header timeout. A provision that
		// is aborted here is retried and starts a second database.
		time.Sleep(hh.RetryResponseHeaderTimeout + time.Second)
		_ = json.NewEncoder(w).Encode(Resource{
			ID:  "db",
			Env: map[string]string{"DATABASE_URL": "postgres://db"},
		})
	}))
	defer srv.Close()

	got, err := Provision(srv.URL+"/databases", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "db" || got.Env["DATABASE_URL"] == "" {
		t.Fatalf("%+v", got)
	}
}

func TestProvisionParsesProviderJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(hh.JSONError{
			Code:    hh.UnknownErrorCode,
			Message: "cannot execute CREATE DATABASE in a read-only transaction",
			Retry:   true,
		})
	}))
	defer srv.Close()

	_, err := Provision(srv.URL+"/databases", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error")
	}
	je, ok := err.(hh.JSONError)
	if !ok {
		t.Fatalf("got %T %v", err, err)
	}
	if !je.Retry || !strings.Contains(je.Message, "CREATE DATABASE") {
		t.Fatalf("%+v", je)
	}
}

func TestProvisionIncludesNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := Provision(srv.URL+"/databases", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("got %v", err)
	}
}

func TestProvisionSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{}` {
			t.Fatalf("config %s", body)
		}
		_ = json.NewEncoder(w).Encode(Resource{
			ID:  "/databases/u:db",
			Env: map[string]string{"DATABASE_URL": "postgres://x"},
		})
	}))
	defer srv.Close()

	got, err := Provision(srv.URL+"/databases", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "/databases/u:db" || got.Env["DATABASE_URL"] != "postgres://x" {
		t.Fatalf("%+v", got)
	}
}

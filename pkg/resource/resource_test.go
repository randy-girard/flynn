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

func TestDeprovisionParsesProviderJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method %s", r.Method)
		}
		if r.URL.Query().Get("id") != "store-uuid" {
			t.Fatalf("id %s", r.URL.Query().Get("id"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(hh.JSONError{
			Code:    hh.ValidationErrorCode,
			Message: "postgres instance not found",
		})
	}))
	defer srv.Close()

	err := Deprovision(srv.URL+"/databases", "store-uuid")
	if err == nil {
		t.Fatal("expected error")
	}
	je, ok := err.(hh.JSONError)
	if !ok {
		t.Fatalf("got %T %v", err, err)
	}
	if je.Code != hh.ValidationErrorCode || !strings.Contains(je.Message, "not found") {
		t.Fatalf("%+v", je)
	}
}

func TestDeprovisionIncludesNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	err := Deprovision(srv.URL+"/databases", "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	je, ok := err.(hh.JSONError)
	if !ok {
		t.Fatalf("got %T %v", err, err)
	}
	if je.Code != hh.ObjectNotFoundErrorCode || !strings.Contains(je.Error(), "404") {
		t.Fatalf("%+v", je)
	}
}

func TestDeprovisionIDsPrefersExternalThenIdentityName(t *testing.T) {
	got := DeprovisionIDs("store-uuid", map[string]string{"FLYNN_POSTGRES": "pg-orchid-xkhthp"})
	if len(got) != 2 || got[0] != "store-uuid" || got[1] != "pg-orchid-xkhthp" {
		t.Fatalf("%v", got)
	}
	got = DeprovisionIDs("pg-orchid-xkhthp", map[string]string{"FLYNN_POSTGRES": "pg-orchid-xkhthp"})
	if len(got) != 1 || got[0] != "pg-orchid-xkhthp" {
		t.Fatalf("dedupe %v", got)
	}
}

func TestDeprovisionAnyRetriesNameAfterNotFound(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		seen = append(seen, id)
		if id == "store-uuid" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(hh.JSONError{
				Code:    hh.ValidationErrorCode,
				Message: "postgres instance not found",
			})
			return
		}
		if id == "pg-orchid-xkhthp" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := DeprovisionAny(srv.URL+"/databases", DeprovisionIDs("store-uuid", map[string]string{
		"FLYNN_POSTGRES": "pg-orchid-xkhthp",
	})...)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "store-uuid" || seen[1] != "pg-orchid-xkhthp" {
		t.Fatalf("seen %v", seen)
	}
}

func TestDeprovisionAnyDoesNotRetryFollowers(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		seen = append(seen, id)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(hh.JSONError{
			Code:    hh.ValidationErrorCode,
			Message: "cannot remove a resource while it still has followers: pg-willow-abcdef",
		})
	}))
	defer srv.Close()

	err := DeprovisionAny(srv.URL+"/databases", "store-uuid", "pg-orchid-xkhthp")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "followers") {
		t.Fatalf("got %v", err)
	}
	if len(seen) != 1 || seen[0] != "store-uuid" {
		t.Fatalf("must not retry name after follower block: %v", seen)
	}
}

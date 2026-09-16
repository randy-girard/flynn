package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostAuthKeyFromRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/host/jobs", nil)
	if got := hostAuthKeyFromRequest(req); got != "" {
		t.Fatalf("%q", got)
	}
	req.Header.Set("Auth-Key", "header-secret")
	if got := hostAuthKeyFromRequest(req); got != "header-secret" {
		t.Fatalf("header=%q", got)
	}
	req = httptest.NewRequest("GET", "/host/jobs", nil)
	req.SetBasicAuth("", "basic-secret")
	if got := hostAuthKeyFromRequest(req); got != "basic-secret" {
		t.Fatalf("basic=%q", got)
	}
}

func TestHostAuthKeyValidConstantTime(t *testing.T) {
	h := &Host{authKey: "cluster-secret"}
	if h.authKeyValid("") || h.authKeyValid("wrong") || h.authKeyValid("cluster-secre") {
		t.Fatal("mismatch must fail")
	}
	if !h.authKeyValid("cluster-secret") {
		t.Fatal("match")
	}
	if (&Host{}).authKeyValid("cluster-secret") {
		t.Fatal("empty configured key must not authenticate")
	}
}

func TestHostAuthMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	open := (&Host{}).authMiddleware(ok)
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, httptest.NewRequest("GET", "/host/jobs", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("no key: %d", rec.Code)
	}

	h := &Host{authKey: "cluster-secret"}
	wrapped := h.authMiddleware(ok)

	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/host/status", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status bypass: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/host/jobs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/host/jobs", nil)
	req.Header.Set("Auth-Key", "cluster-secret")
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("authed: %d", rec.Code)
	}
}

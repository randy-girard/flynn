package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPerIPRateLimiter(t *testing.T) {
	rl := &perIPRateLimiter{requests: make(map[string]int), limit: 2}
	if !rl.Allow("1.1.1.1") || !rl.Allow("1.1.1.1") {
		t.Fatal("first two must pass")
	}
	if rl.Allow("1.1.1.1") {
		t.Fatal("third must be limited")
	}
	if !rl.Allow("8.8.8.8") {
		t.Fatal("other IP is independent")
	}
}

func TestRateLimitMiddlewareDisabledWithoutAuthKey(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	wrapped := (&Host{}).rateLimitMiddleware(next)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/host/jobs", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
}

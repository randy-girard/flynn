package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inconshreveable/log15"
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

	type tc struct {
		name    string
		authKey string
		method  string
		path    string
		remote  string
		header  string
		fwdFor  string
		want    int
	}
	cases := []tc{
		{
			name:   "no key subnet jobs 401",
			method: "GET", path: "/host/jobs", remote: "192.0.2.1:1234",
			want: http.StatusUnauthorized,
		},
		{
			name:   "no key subnet auth-key 401",
			method: "POST", path: "/host/auth-key", remote: "10.0.0.9:9",
			want: http.StatusUnauthorized,
		},
		{
			name:   "no key X-Forwarded-For spoof 401",
			method: "GET", path: "/host/jobs", remote: "198.51.100.9:9",
			fwdFor: "127.0.0.1",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "no key GET status allowed",
			method: "GET", path: "/host/status", remote: "192.0.2.1:1234",
			want: http.StatusNoContent,
		},
		{
			name:   "no key loopback jobs allowed",
			method: "GET", path: "/host/jobs", remote: "127.0.0.1:54321",
			want: http.StatusNoContent,
		},
		{
			name:   "no key IPv6 loopback jobs allowed",
			method: "GET", path: "/host/jobs", remote: "[::1]:9",
			want: http.StatusNoContent,
		},
		{
			name:   "no key unix auth-key allowed",
			method: "POST", path: "/host/auth-key", remote: "/var/run/flynn-host.sock",
			want: http.StatusNoContent,
		},
		{
			name:    "key set GET status bypass",
			authKey: "cluster-secret",
			method:  "GET", path: "/host/status", remote: "192.0.2.1:1234",
			want: http.StatusNoContent,
		},
		{
			name:    "key set unauthed jobs 401",
			authKey: "cluster-secret",
			method:  "GET", path: "/host/jobs", remote: "127.0.0.1:9",
			want: http.StatusUnauthorized,
		},
		{
			name:    "key set loopback not a bypass",
			authKey: "cluster-secret",
			method:  "POST", path: "/host/auth-key", remote: "127.0.0.1:9",
			want: http.StatusUnauthorized,
		},
		{
			name:    "key set Auth-Key header",
			authKey: "cluster-secret",
			method:  "GET", path: "/host/jobs", remote: "10.0.0.2:9",
			header: "cluster-secret",
			want:   http.StatusNoContent,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Host{authKey: c.authKey}
			wrapped := h.authMiddleware(ok)
			req := httptest.NewRequest(c.method, c.path, nil)
			req.RemoteAddr = c.remote
			if c.header != "" {
				req.Header.Set("Auth-Key", c.header)
			}
			if c.fwdFor != "" {
				req.Header.Set("X-Forwarded-For", c.fwdFor)
			}
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status=%d want %d", rec.Code, c.want)
			}
		})
	}
}

func TestConfigureAuthKeyEmptyRejectsSubnet(t *testing.T) {
	api := &jobAPI{host: &Host{log: log15.New()}}
	req := httptest.NewRequest("POST", "/host/auth-key", nil)
	req.RemoteAddr = "10.0.0.8:9"
	rec := httptest.NewRecorder()
	api.ConfigureAuthKey(rec, req, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

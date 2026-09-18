package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/randy-girard/flynn/pkg/status"
)

func TestStatusHandlerPrivateIPBypassesKey(t *testing.T) {
	h := newStatusHandler(status.HealthyHandler, "cluster-secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "100.64.1.2:9"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("overlay IP status=%d", rec.Code)
	}
}

func TestStatusHandlerPublicIPRequiresKey(t *testing.T) {
	h := newStatusHandler(status.HealthyHandler, "cluster-secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "8.8.8.8:9"
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauthed public IP status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "8.8.8.8:9"
	req.SetBasicAuth("", "cluster-secret")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("basic auth status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", status.Path+"?key=cluster-secret", nil)
	req.RemoteAddr = "8.8.8.8:9"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("query key status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "8.8.8.8:9"
	req.SetBasicAuth("", "wrong")
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("wrong key status=%d", rec.Code)
	}
}

func TestStatusHandlerUsesLastForwardedForHop(t *testing.T) {
	h := newStatusHandler(status.HealthyHandler, "cluster-secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "8.8.8.8:9"
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 100.64.9.9")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("last hop overlay IP should be trusted, status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "100.64.1.1:9"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("spoofed last hop must still require the key, status=%d", rec.Code)
	}
}

func TestStatusHandlerNoKeyAllowsAnyone(t *testing.T) {
	h := newStatusHandler(status.HealthyHandler, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", status.Path, nil)
	req.RemoteAddr = "8.8.8.8:9"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestServiceStatusDecodesHealthyJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{"status": "healthy"},
		})
	}))
	defer srv.Close()
	svc := Service{Name: "probe", ReqFn: func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}}
	if got := svc.Status(); got.Status != status.CodeHealthy {
		t.Fatalf("%+v", got)
	}

	bad := Service{Name: "down", ReqFn: func() (*http.Request, error) {
		return http.NewRequest("GET", "http://127.0.0.1:1", nil)
	}}
	if got := bad.Status(); got.Status != status.CodeUnhealthy {
		t.Fatalf("%+v", got)
	}

	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer junk.Close()
	svc = Service{Name: "junk", ReqFn: func() (*http.Request, error) {
		return http.NewRequest("GET", junk.URL, nil)
	}}
	if got := svc.Status(); got.Status != status.CodeUnhealthy {
		t.Fatalf("%+v", got)
	}

	svc = Service{Name: "err", ReqFn: func() (*http.Request, error) {
		return nil, errors.New("no instances")
	}}
	if got := svc.Status(); got.Status != status.CodeUnhealthy {
		t.Fatalf("%+v", got)
	}
}

func TestControllerReqFnWithoutDiscoverd(t *testing.T) {
	if _, err := controllerReqFn(); err == nil {
		t.Fatal("expected error without discoverd")
	}
}

func TestMustParseCIDR(t *testing.T) {
	n := mustParseCIDR("10.0.0.0/8")
	if !n.Contains(mustParseCIDR("10.1.2.3/32").IP) {
		t.Fatal("10/8")
	}
}

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
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

func TestServiceStatusRejectsNon2xxEvenWithHealthyJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{"status": "healthy"},
		})
	}))
	defer srv.Close()
	svc := Service{Name: "auth", ReqFn: func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}}
	if got := svc.Status(); got.Status != status.CodeUnhealthy {
		t.Fatalf("401 must not count as healthy: %+v", got)
	}
}

func TestServiceStatusTriesNextInstance(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer dead.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{"status": "healthy", "version": "ok"},
		})
	}))
	defer live.Close()
	svc := Service{Name: "controller", ReqFns: func() ([]*http.Request, error) {
		r1, err := http.NewRequest("GET", dead.URL, nil)
		if err != nil {
			return nil, err
		}
		r2, err := http.NewRequest("GET", live.URL, nil)
		if err != nil {
			return nil, err
		}
		return []*http.Request{r1, r2}, nil
	}}
	got := svc.Status()
	if got.Status != status.CodeHealthy {
		t.Fatalf("second controller instance must make the service healthy: %+v", got)
	}
}

func TestControllerRequestsOnePerInstance(t *testing.T) {
	if _, err := controllerRequests(nil, "key"); err == nil {
		t.Fatal("expected error with no instances")
	}
	reqs, err := controllerRequests([]*discoverd.Instance{
		{Addr: "10.0.0.1:80"},
		{Addr: "10.0.0.2:80", Meta: map[string]string{"AUTH_KEY": "from-meta"}},
		{Addr: ""},
	}, "env-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("len=%d", len(reqs))
	}
	if got := reqs[0].URL.Host; got != "10.0.0.1:80" {
		t.Fatalf("first host %s", got)
	}
	if got := reqs[1].URL.Host; got != "10.0.0.2:80" {
		t.Fatalf("second host %s", got)
	}
	if _, pass, ok := reqs[0].BasicAuth(); !ok || pass != "env-key" {
		t.Fatalf("env CONTROLLER_KEY must authenticate every instance, got %q", pass)
	}
	reqs, err = controllerRequests([]*discoverd.Instance{
		{Addr: "10.0.0.2:80", Meta: map[string]string{"AUTH_KEY": "from-meta"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, pass, ok := reqs[0].BasicAuth(); !ok || pass != "from-meta" {
		t.Fatalf("empty CONTROLLER_KEY must fall back to instance AUTH_KEY, got %q", pass)
	}
}

func TestMustParseCIDR(t *testing.T) {
	n := mustParseCIDR("10.0.0.0/8")
	if !n.Contains(mustParseCIDR("10.1.2.3/32").IP) {
		t.Fatal("10/8")
	}
}

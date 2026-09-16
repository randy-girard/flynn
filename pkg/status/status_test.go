package status

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHealthyAndUnhealthyHandlers(t *testing.T) {
	os.Unsetenv("FLYNN_VERSION")
	rec := httptest.NewRecorder()
	HealthyHandler.ServeHTTP(rec, httptest.NewRequest("GET", Path, nil))
	if rec.Code != 200 {
		t.Fatalf("healthy status %d", rec.Code)
	}
	var wrap struct {
		Data Status `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrap); err != nil {
		t.Fatal(err)
	}
	if wrap.Data.Status != CodeHealthy {
		t.Fatalf("%+v", wrap.Data)
	}

	rec = httptest.NewRecorder()
	Handler(func() Status { return Unhealthy }).ServeHTTP(rec, httptest.NewRequest("GET", Path, nil))
	if rec.Code != 500 {
		t.Fatalf("unhealthy status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	SimpleHandler(func() error { return errors.New("down") }).ServeHTTP(rec, httptest.NewRequest("GET", Path, nil))
	if rec.Code != 500 {
		t.Fatal("simple unhealthy")
	}
	rec = httptest.NewRecorder()
	SimpleHandler(func() error { return nil }).ServeHTTP(rec, httptest.NewRequest("GET", Path, nil))
	if rec.Code != 200 {
		t.Fatal("simple healthy")
	}
}

func TestNewStatusDetailAndEmptyCode(t *testing.T) {
	s, err := New(true, map[string]string{"db": "up"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != CodeHealthy || s.Detail == nil {
		t.Fatalf("%+v", s)
	}
	s, err = New(false, nil)
	if err != nil || s.Status != CodeUnhealthy {
		t.Fatalf("%+v %v", s, err)
	}

	rec := httptest.NewRecorder()
	Handler(func() Status { return Status{} }).ServeHTTP(rec, httptest.NewRequest("GET", Path, nil))
	if rec.Code != 500 {
		t.Fatalf("empty code should be unhealthy, got %d", rec.Code)
	}
}

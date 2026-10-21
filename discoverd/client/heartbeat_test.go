package discoverd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func TestMissingServiceIgnoresInstanceNotFound(t *testing.T) {
	serviceGone := hh.JSONError{Code: hh.ObjectNotFoundErrorCode, Message: `discoverd: service "mysql-lake" not found`}
	instanceGone := hh.JSONError{Code: hh.ObjectNotFoundErrorCode, Message: `discoverd: instance mysql-lake/abc not found`}
	if !missingService(serviceGone) {
		t.Fatal("a missing service must be recreated")
	}
	if missingService(instanceGone) {
		t.Fatal("a missing instance must not create a service")
	}
	if missingService(hh.JSONError{Code: hh.ObjectExistsErrorCode, Message: "service exists"}) {
		t.Fatal("exists is not a missing service")
	}
}

func TestRegisterRecreatesManualServiceAndLeader(t *testing.T) {
	var calls []string
	var created ServiceConfig
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/services/mysql-lake/instances/"):
			if len(calls) == 1 {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(hh.JSONError{
					Code:    hh.ObjectNotFoundErrorCode,
					Message: `discoverd: service "mysql-lake" not found`,
				})
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == "PUT" && r.URL.Path == "/services/mysql-lake":
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &created); err != nil {
				t.Errorf("config: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == "PUT" && r.URL.Path == "/services/mysql-lake/leader":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL(srv.URL)
	inst := &Instance{ID: "abc", Addr: "10.0.0.1:3306", Proto: "tcp"}
	h := newHeartbeater(client, "mysql-lake", inst)
	h.serviceConfig = &ServiceConfig{LeaderType: LeaderTypeManual}
	h.claimLeader = true
	if err := h.registerInstance(); err != nil {
		t.Fatal(err)
	}
	if created.LeaderType != LeaderTypeManual {
		t.Fatalf("leader type %q", created.LeaderType)
	}
	want := []string{
		"PUT /services/mysql-lake/instances/abc",
		"PUT /services/mysql-lake",
		"PUT /services/mysql-lake/instances/abc",
		"PUT /services/mysql-lake/leader",
	}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls %#v", calls)
	}
}

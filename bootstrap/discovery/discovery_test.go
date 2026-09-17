package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetCluster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/instances" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []Instance{{ID: "i1", Name: "node1"}},
		})
	}))
	defer srv.Close()

	got, err := GetCluster(srv.URL)
	if err != nil || len(got) != 1 || got[0].ID != "i1" {
		t.Fatalf("%v %v", got, err)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	if _, err := GetCluster(fail.URL); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v", err)
	}
}

func TestRegisterInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/instances" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Data Instance `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Data.Name != "node1" || body.Data.URL != "http://10.0.0.1" {
			t.Errorf("%+v", body.Data)
		}
		w.WriteHeader(http.StatusCreated)
		body.Data.ID = "assigned-id"
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	id, err := RegisterInstance(Info{ClusterURL: srv.URL, InstanceURL: "http://10.0.0.1", Name: "node1"})
	if err != nil || id != "assigned-id" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestNewTokenRequiresDiscoveryServer(t *testing.T) {
	t.Setenv("DISCOVERY_SERVER", "")
	_, err := NewToken()
	if err == nil || !strings.Contains(err.Error(), "DISCOVERY_SERVER") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), ExampleServer) {
		t.Fatalf("error should cite example URL: %v", err)
	}
}

func TestNewTokenUsesDiscoveryServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/clusters" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		w.Header().Set("Location", "/clusters/tok-1")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	t.Setenv("DISCOVERY_SERVER", srv.URL)

	got, err := NewToken()
	if err != nil || !strings.HasSuffix(got, "/clusters/tok-1") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestURLError(t *testing.T) {
	err := urlError("GET", "http://x", 418)
	if err == nil || !strings.Contains(err.Error(), "418") {
		t.Fatalf("%v", err)
	}
}

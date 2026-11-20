package v1controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

// TestSetTenancyModeBody: the mode PUT sends {"mode": m} and adds
// "force": true only when the operator asked for the override.
func TestSetTenancyModeBody(t *testing.T) {
	var bodies []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/tenancy" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Errorf("body %q: %v", raw, err)
		}
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ct.TenancySettings{Mode: m["mode"].(string)})
	}))
	defer srv.Close()

	c := &Client{Client: &httpclient.Client{URL: srv.URL, Key: "k", HTTP: srv.Client()}}
	s, err := c.SetTenancyMode("self_hosted", false)
	if err != nil || s.Mode != "self_hosted" {
		t.Fatalf("settings %+v err %v", s, err)
	}
	if _, err := c.SetTenancyMode("self_hosted", true); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies %v", bodies)
	}
	if len(bodies[0]) != 1 || bodies[0]["mode"] != "self_hosted" {
		t.Fatalf("plain body %v", bodies[0])
	}
	if len(bodies[1]) != 2 || bodies[1]["force"] != true {
		t.Fatalf("force body %v", bodies[1])
	}
}

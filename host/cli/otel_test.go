package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestLookupOTELPlugin(t *testing.T) {
	_, err := lookupOTELPlugin(nil)
	if err == nil || !strings.Contains(err.Error(), "plugin:install otel") {
		t.Fatalf("missing plugin: %v", err)
	}
	app := &ct.App{Name: "otel", Meta: map[string]string{
		plugin.MetaPlugin:     "true",
		plugin.MetaPluginWait: "http://otel.discoverd/.well-known/status",
	}}
	got, err := lookupOTELPlugin([]*ct.App{app})
	if err != nil || got.Name != "otel" {
		t.Fatalf("%v %v", got, err)
	}
	if base := otelPluginBase(got); base != "http://otel.discoverd" {
		t.Fatalf("base=%s", base)
	}
	legacy := &ct.App{Name: "opentelemetry", Meta: map[string]string{
		plugin.MetaPlugin:     "true",
		plugin.MetaPluginWait: "http://opentelemetry.discoverd/.well-known/status",
	}}
	got, err = lookupOTELPlugin([]*ct.App{legacy})
	if err != nil || got.Name != "opentelemetry" {
		t.Fatalf("alias: %v %v", got, err)
	}
}

func TestOTELHTTPClient(t *testing.T) {
	const key = "cluster-key"
	mux := http.NewServeMux()
	requireKey := func(w http.ResponseWriter, r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "" || pass != key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /exporters", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode([]otelExporter{{ID: "e1", Endpoint: "http://alloy:4318"}})
	})
	mux.HandleFunc("POST /exporters", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		var in otelExporter
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Headers["Authorization"] == "Basic "+key || in.Headers["Authorization"] == "Bearer "+key {
			t.Error("collector Authorization header must not be the Flynn cluster key")
		}
		in.ID = "new"
		_ = json.NewEncoder(w).Encode(in)
	})
	mux.HandleFunc("DELETE /exporters/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		if r.PathValue("id") != "e1" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if _, err := otelList(ts.Client(), ts.URL, ""); err == nil {
		t.Fatal("expected error without cluster key")
	}
	if _, err := otelList(ts.Client(), ts.URL, "wrong"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected HTTP 401 with wrong key, got %v", err)
	}

	rows, err := otelList(ts.Client(), ts.URL, key)
	if err != nil || len(rows) != 1 || rows[0].ID != "e1" {
		t.Fatalf("%v %v", rows, err)
	}
	created, err := otelCreate(ts.Client(), ts.URL, key, otelExporter{
		Endpoint: "http://x:4318",
		Headers:  map[string]string{"Authorization": "Bearer tok"},
		Insecure: true,
	})
	if err != nil || created.ID != "new" || !created.Insecure {
		t.Fatalf("%+v %v", created, err)
	}
	if created.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("collector --auth headers must pass through, got %v", created.Headers)
	}
	if err := otelDelete(ts.Client(), ts.URL, key, "e1"); err != nil {
		t.Fatal(err)
	}
}

func TestOTELClusterKeyEnv(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "from-controller")
	t.Setenv("AUTH_KEY", "from-auth")
	got, err := otelClusterKey()
	if err != nil || got != "from-controller" {
		t.Fatalf("CONTROLLER_KEY: %q %v", got, err)
	}
	t.Setenv("CONTROLLER_KEY", "")
	got, err = otelClusterKey()
	if err != nil || got != "from-auth" {
		t.Fatalf("AUTH_KEY fallback: %q %v", got, err)
	}
}

func TestParseOTELHeaders(t *testing.T) {
	h, err := parseOTELHeaders([]string{"Authorization: Bearer x"})
	if err != nil || h["Authorization"] != "Bearer x" {
		t.Fatalf("%v %v", h, err)
	}
	if _, err := parseOTELHeaders([]string{"nope"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestOTELAuthHeaders(t *testing.T) {
	args := parseHostCLI(t, "otel:add", []string{"otel:add", "--auth", "bearer", "--token", "tok", "https://otlp.example/otlp"})
	h, err := otelAuthHeaders(args)
	if err != nil || h["Authorization"] != "Bearer tok" {
		t.Fatalf("%v %v", h, err)
	}
	args = parseHostCLI(t, "otel:add", []string{"otel:add", "--username", "user", "--password", "pass", "https://otlp.example/otlp"})
	h, err = otelAuthHeaders(args)
	if err != nil || h["Authorization"] != "Basic dXNlcjpwYXNz" {
		t.Fatalf("inferred basic: %v %v", h, err)
	}
	if otelAuthKind(map[string]string{"Authorization": "Bearer x"}) != "bearer" {
		t.Fatal("kind bearer")
	}
	if otelAuthKind(nil) != "none" {
		t.Fatal("kind none")
	}
}

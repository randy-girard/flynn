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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /exporters", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]otelExporter{{ID: "e1", Endpoint: "http://alloy:4318"}})
	})
	mux.HandleFunc("POST /exporters", func(w http.ResponseWriter, r *http.Request) {
		var in otelExporter
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.ID = "new"
		_ = json.NewEncoder(w).Encode(in)
	})
	mux.HandleFunc("DELETE /exporters/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "e1" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	rows, err := otelList(ts.Client(), ts.URL)
	if err != nil || len(rows) != 1 || rows[0].ID != "e1" {
		t.Fatalf("%v %v", rows, err)
	}
	created, err := otelCreate(ts.Client(), ts.URL, otelExporter{Endpoint: "http://x:4318", Insecure: true})
	if err != nil || created.ID != "new" || !created.Insecure {
		t.Fatalf("%+v %v", created, err)
	}
	if err := otelDelete(ts.Client(), ts.URL, "e1"); err != nil {
		t.Fatal(err)
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

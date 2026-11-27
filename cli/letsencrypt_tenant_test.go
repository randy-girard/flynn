package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

// tenantLetsEncryptServer is the controller as a non-admin tenant sees it
// (NEW-29): GET /apps lists only the tenant's app, never letsencrypt-plugin;
// the plugin shows up in GET /plugin-catalog; GET /acme/config is the
// tenant view (enabled only).
func tenantLetsEncryptServer(t *testing.T) (controller.Client, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		var out interface{}
		switch r.Method + " " + r.URL.Path {
		case "GET /apps":
			out = []*ct.App{{ID: "a1", Name: "shop"}}
		case "GET /plugin-catalog":
			out = map[string]interface{}{"commands": []map[string]interface{}{{"command": "letsencrypt", "user": true, "app": "letsencrypt-plugin"}}}
		case "GET /acme/config":
			out = ct.ACMEConfig{Enabled: true}
		case "GET /routes":
			out = []*router.Route{{Type: "http", ID: "r1", Domain: "shop.example.com", ParentRef: ct.RouteParentRefPrefix + "a1"}}
		case "PUT /apps/shop/routes/http/r1":
			w.Write(body)
			return
		default:
			w.WriteHeader(http.StatusForbidden)
			out = map[string]string{"code": "forbidden", "message": "not for tenants"}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	c, err := controller.NewClientWithToken(srv.URL, "tenant-token")
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func TestLetsEncryptEnableWorksForTenant(t *testing.T) {
	c, calls := tenantLetsEncryptServer(t)
	prev := flagApp
	flagApp = "shop"
	defer func() { flagApp = prev }()
	if err := toggleLetsEncrypt(c, "shop.example.com", true); err != nil {
		t.Fatalf("tenant enable: %s (calls %v)", err, *calls)
	}
	if !strings.Contains(strings.Join(*calls, ","), "PUT /apps/shop/routes/http/r1") {
		t.Fatalf("no route PUT: %v", *calls)
	}
}

func TestLetsEncryptStatusWorksForTenant(t *testing.T) {
	c, calls := tenantLetsEncryptServer(t)
	prev := flagApp
	flagApp = "shop"
	defer func() { flagApp = prev }()
	out := captureStdout(t, func() {
		if err := runLetsEncryptStatus(parseCLI(t, []string{"letsencrypt:status"}), c); err != nil {
			t.Fatalf("tenant status: %s (calls %v)", err, *calls)
		}
	})
	if !strings.Contains(out, "cluster enabled: true") {
		t.Fatalf("status output %q", out)
	}
}

func TestLetsEncryptReportsMissingPluginFromCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/plugin-catalog":
			w.Write([]byte(`{"commands":[]}`))
		case "/apps":
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	c, err := controller.NewClientWithToken(srv.URL, "tenant-token")
	if err != nil {
		t.Fatal(err)
	}
	err = requireLetsEncryptPlugin(c)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var b bytes.Buffer
	io.Copy(&b, r)
	return b.String()
}

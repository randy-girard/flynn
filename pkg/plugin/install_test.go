package plugin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	router "github.com/randy-girard/flynn/router/types"
)

func writeAppPlugin(t *testing.T, dir, name string) {
	t.Helper()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": name,
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/" + name}},
			},
		},
	})
}

func TestInstallRejectsGitHubRebuild(t *testing.T) {
	err := (&Installer{}).Install(InstallOptions{
		Source:  "https://github.com/acme/flynn-plugin-redis.git",
		Rebuild: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--rebuild") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallRefusesIncompatiblePluginRef(t *testing.T) {
	in := &Installer{FlynnVersion: "v20260919.0"}
	err := in.Install(InstallOptions{
		Source: "https://github.com/acme/flynn-plugin-redis.git",
		Ref:    "v20260920.0.1",
	})
	if err == nil || !strings.Contains(err.Error(), "v20260920.0") || !strings.Contains(err.Error(), "v20260919.0") {
		t.Fatalf("newer Flynn plugin must be refused: %v", err)
	}
	err = in.Update(InstallOptions{
		Source: "https://github.com/acme/flynn-plugin-redis.git",
		Ref:    "v1",
	})
	if err == nil || !strings.Contains(err.Error(), "v1") {
		t.Fatalf("non-calver --ref must be refused on a calver cluster: %v", err)
	}
	if err := in.refuseIncompatiblePlugin("v20260919.0.4"); err != nil {
		t.Fatalf("compatible patch must be allowed: %v", err)
	}
	dev := &Installer{FlynnVersion: "dev"}
	if err := dev.refuseIncompatiblePlugin("v20260920.0.1"); err != nil {
		t.Fatalf("dev Flynn skips calver refuse: %v", err)
	}
}

func TestInstallNoBuildWithoutDist(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	err := (&Installer{}).Install(InstallOptions{Source: dir, NoBuild: true, Yes: true})
	if err == nil || !strings.Contains(err.Error(), "--no-build") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallMissingBuildScript(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	err := (&Installer{Stderr: io.Discard}).Install(InstallOptions{Source: dir, Yes: true})
	if err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallInvokesBuildHook(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	called := false
	err := (&Installer{
		Build: func(root string) error {
			called = true
			if root != dir {
				t.Fatalf("root=%s want %s", root, dir)
			}
			return fmt.Errorf("stop")
		},
	}).Install(InstallOptions{Source: dir, Yes: true})
	if !called {
		t.Fatal("Build was not called")
	}
	if err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitHTTP(srv.Client(), srv.URL, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitHTTP(http.DefaultClient, "http://127.0.0.1:1", 0); err == nil {
		t.Fatal("zero timeout must fail")
	}
}

func TestPutFileAndBytes(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method=%s", r.Method)
		}
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "layer.squashfs")
	if err := os.WriteFile(path, []byte("layer-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := putFile(srv.Client(), srv.URL+"/layers/a.squashfs", path); err != nil {
		t.Fatal(err)
	}
	if string(got) != "layer-bytes" {
		t.Fatalf("putFile body=%q", got)
	}
	if err := putBytes(srv.Client(), srv.URL+"/manifest.json", []byte("manifest")); err != nil {
		t.Fatal(err)
	}
	if string(got) != "manifest" {
		t.Fatalf("putBytes body=%q", got)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	if err := putBytes(fail.Client(), fail.URL, []byte("x")); err == nil {
		t.Fatal("500 must fail")
	}
}

func TestDisplaySourceAndLogf(t *testing.T) {
	if got := displaySource(&Resolved{GitHub: &GitHubSource{Owner: "acme", Repo: "plug", Ref: "v1"}}, "/tmp"); got != "acme/plug@v1" {
		t.Fatalf("github display=%s", got)
	}
	if got := displaySource(&Resolved{}, "/tmp/plugin"); got != "/tmp/plugin" {
		t.Fatalf("local display=%s", got)
	}
	if got := displaySource(nil, "/tmp/plugin"); got != "/tmp/plugin" {
		t.Fatalf("nil display=%s", got)
	}

	in := &Installer{}
	in.logf("hello %s", "world")
	var buf bytes.Buffer
	in.Stdout = &buf
	in.logf("hello %s", "world")
	if !strings.Contains(buf.String(), "hello world") {
		t.Fatalf("log=%q", buf.String())
	}
}

func TestWaitHTTPRetriesThenOK(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitHTTP(srv.Client(), srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("retries=%d", n)
	}
}

func TestPutFileMissingAndRunHookSecrets(t *testing.T) {
	if err := putFile(http.DefaultClient, "http://127.0.0.1/x", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file must fail")
	}

	root := t.TempDir()
	script := filepath.Join(root, "install.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nset -e\ntest \"$CONTROLLER_KEY\" = secret\ntest -n \"$FLYNN_PLUGIN_NAME\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Stdout: io.Discard, Stderr: io.Discard}
	m := &Manifest{Name: "widget", Kind: KindApp}
	if err := in.runHook(root, m, "install.sh", map[string]string{"CONTROLLER_KEY": "secret", "CLUSTER_DOMAIN": "example.local"}); err != nil {
		t.Fatal(err)
	}
	if err := in.runHook(root, m, "install.sh", map[string]string{"CONTROLLER_KEY": "wrong"}); err == nil {
		t.Fatal("hook must see injected CONTROLLER_KEY")
	}
}

func TestInstallHookAndRunHook(t *testing.T) {
	m := &Manifest{Name: "widget", Kind: KindApp}
	if m.installHook() != "" {
		t.Fatal("empty hooks")
	}
	m.Hooks = &Hooks{Install: "hooks/install.sh", Upgrade: "hooks/upgrade.sh", Uninstall: "hooks/uninstall.sh", Ready: "hooks/ready.sh"}
	if m.installHook() != "hooks/install.sh" {
		t.Fatal(m.installHook())
	}
	if m.upgradeHook() != "hooks/upgrade.sh" {
		t.Fatal(m.upgradeHook())
	}
	if m.readyHook() != "hooks/ready.sh" {
		t.Fatal(m.readyHook())
	}
	if m.deployHook(false) != "hooks/install.sh" {
		t.Fatal("first install must run hooks.install")
	}
	if m.deployHook(true) != "hooks/upgrade.sh" {
		t.Fatal("update must run hooks.upgrade")
	}
	m.Hooks.Upgrade = ""
	if m.deployHook(true) != "" {
		t.Fatal("update must not re-run hooks.install when upgrade is unset")
	}
	if m.uninstallHook() != "hooks/uninstall.sh" {
		t.Fatal(m.uninstallHook())
	}
	in := &Installer{}
	if err := in.runHook(t.TempDir(), m, "", nil); err != nil {
		t.Fatal(err)
	}
	err := in.runHook(t.TempDir(), m, "hooks/missing.sh", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "hook") {
		t.Fatalf("missing hook must fail, got %v", err)
	}
}

type providerStub struct {
	list      []*ct.Provider
	listErr   error
	created   *ct.Provider
	createErr error
}

func (s *providerStub) ProviderList() ([]*ct.Provider, error) {
	return s.list, s.listErr
}

func (s *providerStub) CreateProvider(p *ct.Provider) error {
	s.created = p
	return s.createErr
}

func TestEnsureProviderIdempotentAndCreates(t *testing.T) {
	existing := &providerStub{list: []*ct.Provider{{Name: "redis", URL: "http://redis-api.discoverd"}}}
	if err := ensureProvider(existing, "redis", "http://other"); err != nil {
		t.Fatal(err)
	}
	if existing.created != nil {
		t.Fatal("must not recreate an existing provider")
	}

	created := &providerStub{}
	if err := ensureProvider(created, "redis", "http://redis-api.discoverd/clusters"); err != nil {
		t.Fatal(err)
	}
	if created.created == nil || created.created.Name != "redis" || created.created.URL != "http://redis-api.discoverd/clusters" {
		t.Fatalf("%+v", created.created)
	}

	if err := ensureProvider(&providerStub{listErr: fmt.Errorf("down")}, "redis", "http://x"); err == nil {
		t.Fatal("list error")
	}
	if err := ensureProvider(&providerStub{createErr: fmt.Errorf("denied")}, "redis", "http://x"); err == nil {
		t.Fatal("create error")
	}
}

func TestInstallerHTTPAndRunBuildMissing(t *testing.T) {
	in := &Installer{}
	if in.http() != http.DefaultClient {
		t.Fatal("default HTTP client")
	}
	in.persistInventory() // nil Client must be a no-op
	if err := in.runBuild(t.TempDir()); err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("missing plugin-build script: %v", err)
	}
}

type webhookHostStub struct {
	id        string
	listed    []*host.WebhookConfig
	listErr   error
	added     []webhookAdd
	addErr    error
	removed   []string
	removeErr error
}

type webhookAdd struct {
	id      string
	url     string
	headers map[string]string
}

func (s *webhookHostStub) ID() string { return s.id }

func (s *webhookHostStub) ListWebhooks() ([]*host.WebhookConfig, error) {
	return s.listed, s.listErr
}

func (s *webhookHostStub) AddWebhook(id, url string, headers map[string]string) (*host.WebhookConfig, error) {
	s.added = append(s.added, webhookAdd{id: id, url: url, headers: headers})
	if s.addErr != nil {
		return nil, s.addErr
	}
	return &host.WebhookConfig{ID: id, URL: url, Headers: headers}, nil
}

func (s *webhookHostStub) RemoveWebhook(id string) error {
	s.removed = append(s.removed, id)
	if s.removeErr != nil {
		return s.removeErr
	}
	return nil
}

func TestExpandWebhookSpecSecretEnv(t *testing.T) {
	url, headers, err := expandWebhookSpec(WebhookSpec{
		URL:       "http://${APP}.discoverd/webhooks/flynn",
		SecretEnv: "WEBHOOK_INGEST_SECRET",
		Headers:   map[string]string{"X-Extra": "v-${CLUSTER_DOMAIN}"},
	}, map[string]string{
		"APP":                   "dashboard",
		"WEBHOOK_INGEST_SECRET": "s3cret",
		"CLUSTER_DOMAIN":        "ex.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://dashboard.discoverd/webhooks/flynn" {
		t.Fatalf("url=%q", url)
	}
	if headers["X-Flynn-Webhook-Secret"] != "s3cret" || headers["X-Extra"] != "v-ex.local" {
		t.Fatalf("headers=%v", headers)
	}
	_, _, err = expandWebhookSpec(WebhookSpec{URL: "http://x", SecretEnv: "WEBHOOK_INGEST_SECRET"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "WEBHOOK_INGEST_SECRET") {
		t.Fatalf("empty secret: %v", err)
	}

	url, headers, err = expandWebhookSpec(WebhookSpec{
		URL:       "http://widget.discoverd/hook",
		SecretEnv: "WEBHOOK_INGEST_SECRET",
		Headers:   map[string]string{"X-Flynn-Webhook-Secret": "explicit"},
	}, map[string]string{"WEBHOOK_INGEST_SECRET": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if headers["X-Flynn-Webhook-Secret"] != "explicit" {
		t.Fatalf("explicit secret header must win, got %v", headers)
	}

	url, headers, err = expandWebhookSpec(WebhookSpec{URL: "http://widget.discoverd/hook"}, nil)
	if err != nil || url != "http://widget.discoverd/hook" || headers != nil {
		t.Fatalf("no headers: url=%q headers=%v err=%v", url, headers, err)
	}

	_, _, err = expandWebhookSpec(WebhookSpec{URL: "ftp://widget.discoverd/hook"}, nil)
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("ftp: %v", err)
	}
	_, _, err = expandWebhookSpec(WebhookSpec{URL: "${MISSING}"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("unexpanded: %v", err)
	}
	_, _, err = expandWebhookSpec(WebhookSpec{URL: "${EMPTY}"}, map[string]string{"EMPTY": ""})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty expand: %v", err)
	}
}

func TestEnsureWebhooksRegistersAndIsIdempotent(t *testing.T) {
	stub := &webhookHostStub{id: "node1"}
	in := &Installer{
		Stdout: io.Discard,
		Hosts:  func() ([]WebhookHost, error) { return []WebhookHost{stub}, nil },
	}
	m := &Manifest{
		Name: "dashboard",
		Webhooks: []WebhookSpec{{
			URL:       "http://dashboard.discoverd/webhooks/flynn",
			SecretEnv: "WEBHOOK_INGEST_SECRET",
		}},
	}
	cluster := map[string]string{"WEBHOOK_INGEST_SECRET": "s3cret"}
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(stub.added) != 1 || stub.added[0].url != "http://dashboard.discoverd/webhooks/flynn" {
		t.Fatalf("added=%+v", stub.added)
	}
	if stub.added[0].headers["X-Flynn-Webhook-Secret"] != "s3cret" {
		t.Fatalf("headers=%v", stub.added[0].headers)
	}
	wantID := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	if stub.added[0].id != wantID {
		t.Fatalf("id=%s want %s", stub.added[0].id, wantID)
	}

	stub.listed = []*host.WebhookConfig{{ID: "manual", URL: "http://dashboard.discoverd/webhooks/flynn"}}
	stub.added = nil
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(stub.added) != 0 {
		t.Fatalf("must not duplicate an existing URL, added=%+v", stub.added)
	}

	if err := in.ensureWebhooks(&Manifest{Name: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	in.Hosts = func() ([]WebhookHost, error) { return nil, nil }
	if err := in.ensureWebhooks(m, cluster); err == nil {
		t.Fatal("no hosts must fail")
	}

	stub2 := &webhookHostStub{id: "node1"}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{stub2}, fmt.Errorf("discoverd down") }
	if err := in.ensureWebhooks(m, cluster); err == nil || !strings.Contains(err.Error(), "discoverd down") {
		t.Fatalf("hosts error: %v", err)
	}

	failAdd := &webhookHostStub{id: "node1", addErr: fmt.Errorf("denied")}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{failAdd}, nil }
	if err := in.ensureWebhooks(m, cluster); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("add error: %v", err)
	}

	failList := &webhookHostStub{id: "node1", listErr: fmt.Errorf("list failed")}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{failList}, nil }
	if err := in.ensureWebhooks(m, cluster); err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("list error: %v", err)
	}

	n1 := &webhookHostStub{id: "n1"}
	n2 := &webhookHostStub{id: "n2"}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{n1, n2}, nil }
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(n1.added) != 1 || len(n2.added) != 1 {
		t.Fatalf("must register on every host n1=%d n2=%d", len(n1.added), len(n2.added))
	}

	sameID := &webhookHostStub{
		id:     "node1",
		listed: []*host.WebhookConfig{{ID: wantID, URL: "http://dashboard.discoverd/webhooks/flynn"}},
	}
	in.Hosts = func() ([]WebhookHost, error) { return []WebhookHost{sameID}, nil }
	if err := in.ensureWebhooks(m, cluster); err != nil {
		t.Fatal(err)
	}
	if len(sameID.added) != 1 {
		t.Fatalf("same plugin id must upsert headers, added=%d", len(sameID.added))
	}
}

func TestPluginWebhookIDStable(t *testing.T) {
	a := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	b := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	c := pluginWebhookID("other", "http://dashboard.discoverd/webhooks/flynn")
	if a != b || !strings.HasPrefix(a, "plugin-dashboard-") {
		t.Fatalf("id=%s", a)
	}
	if a == c {
		t.Fatal("plugin name must be part of the id")
	}
}

type routeStub struct {
	routes    []*router.Route
	created   []*router.Route
	updated   []*router.Route
	deleted   []string
	acme      *ct.ACMEConfig
	acmeErr   error
	listErr   error
	createErr error
	updateErr error
	deleteErr error
}

func (s *routeStub) AppRouteList(string) ([]*router.Route, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.routes, nil
}

func (s *routeStub) CreateRoute(_ string, route *router.Route) error {
	if s.createErr != nil {
		return s.createErr
	}
	cp := *route
	if cp.ID == "" {
		cp.ID = fmt.Sprintf("r%d", len(s.created)+1)
	}
	s.created = append(s.created, &cp)
	s.routes = append(s.routes, &cp)
	return nil
}

func (s *routeStub) UpdateRoute(_ string, routeID string, route *router.Route) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	cp := *route
	s.updated = append(s.updated, &cp)
	for i, r := range s.routes {
		if r == nil {
			continue
		}
		if r.FormattedID() == routeID || r.ID == routeID || (route.ID != "" && r.ID == route.ID) {
			s.routes[i] = &cp
			break
		}
	}
	return nil
}

func (s *routeStub) DeleteRoute(_ string, routeID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	found := false
	out := s.routes[:0]
	for _, r := range s.routes {
		if r != nil && (r.FormattedID() == routeID || r.ID == routeID) {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("route %s not found", routeID)
	}
	s.routes = out
	s.deleted = append(s.deleted, routeID)
	return nil
}

func (s *routeStub) GetACMEConfig() (*ct.ACMEConfig, error) {
	if s.acmeErr != nil {
		return nil, s.acmeErr
	}
	if s.acme == nil {
		return &ct.ACMEConfig{Enabled: false}, nil
	}
	return s.acme, nil
}

func TestHasHTTPRoute(t *testing.T) {
	if hasHTTPRoute(nil) || hasHTTPRoute(&Manifest{}) {
		t.Fatal("empty")
	}
	if hasHTTPRoute(&Manifest{Routes: []RouteSpec{{Type: "tcp", Service: "db"}}}) {
		t.Fatal("tcp only")
	}
	if !hasHTTPRoute(&Manifest{Routes: []RouteSpec{{Service: "web", Domain: "x.example"}}}) {
		t.Fatal("default type is http")
	}
}

func TestEnsureRoutesAutoTLS(t *testing.T) {
	app := &ct.App{ID: "app1", Name: "widget"}
	cluster := map[string]string{"CLUSTER_DOMAIN": "ex.local"}
	m := &Manifest{
		Name: "widget",
		Kind: KindApp,
		App:  AppSpec{Name: "widget"},
		Routes: []RouteSpec{
			{Type: "http", Domain: "widget.${CLUSTER_DOMAIN}", Service: "widget", AutoTLS: true},
		},
	}

	stub := &routeStub{acme: &ct.ACMEConfig{Enabled: true}}
	in := &Installer{Stdout: io.Discard, RouteClient: stub}
	if err := in.ensureRoutes(app, m, cluster, false); err != nil {
		t.Fatal(err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("created=%d", len(stub.created))
	}
	if !routeHasAutoTLS(stub.created[0]) || *stub.created[0].ManagedCertificateDomain != "widget.ex.local" {
		t.Fatalf("route %+v", stub.created[0])
	}

	warn := &bytes.Buffer{}
	noACME := &routeStub{}
	in = &Installer{Stdout: warn, RouteClient: noACME}
	if err := in.ensureRoutes(app, m, cluster, false); err != nil {
		t.Fatal(err)
	}
	if len(noACME.created) != 1 || routeHasAutoTLS(noACME.created[0]) {
		t.Fatalf("manifest auto_tls without ACME must still create HTTP: %+v", noACME.created)
	}
	if !strings.Contains(warn.String(), "skipping auto TLS") {
		t.Fatalf("warn=%q", warn.String())
	}

	fail := &routeStub{}
	in = &Installer{Stdout: io.Discard, RouteClient: fail}
	err := in.ensureRoutes(app, m, cluster, true)
	if err == nil || !strings.Contains(err.Error(), "ACME") {
		t.Fatalf("--auto-tls without ACME must fail, got %v", err)
	}
	if len(fail.created) != 0 {
		t.Fatal("must not create routes when --auto-tls cannot run")
	}
}

func TestEnsureRoutesAutoTLSUpdatesExisting(t *testing.T) {
	app := &ct.App{ID: "app1", Name: "widget"}
	cluster := map[string]string{"CLUSTER_DOMAIN": "ex.local"}
	m := &Manifest{
		Routes: []RouteSpec{
			{Type: "http", Domain: "widget.${CLUSTER_DOMAIN}", Service: "widget", AutoTLS: true},
		},
	}
	existing := &router.Route{Type: "http", ID: "abc", Domain: "widget.ex.local", Service: "widget"}
	stub := &routeStub{
		routes: []*router.Route{existing},
		acme:   &ct.ACMEConfig{Enabled: true},
	}
	in := &Installer{Stdout: io.Discard, RouteClient: stub}
	if err := in.ensureRoutes(app, m, cluster, false); err != nil {
		t.Fatal(err)
	}
	if len(stub.created) != 0 || len(stub.updated) != 1 {
		t.Fatalf("created=%d updated=%d", len(stub.created), len(stub.updated))
	}
	if !routeHasAutoTLS(stub.updated[0]) {
		t.Fatalf("updated %+v", stub.updated[0])
	}

	already := &router.Route{Type: "http", ID: "abc", Domain: "widget.ex.local", Service: "widget"}
	d := already.Domain
	already.ManagedCertificateDomain = &d
	stub = &routeStub{
		routes: []*router.Route{already},
		acme:   &ct.ACMEConfig{Enabled: true},
	}
	in = &Installer{Stdout: io.Discard, RouteClient: stub}
	if err := in.ensureRoutes(app, m, cluster, true); err != nil {
		t.Fatal(err)
	}
	if len(stub.updated) != 0 {
		t.Fatal("already-managed cert must not be updated again")
	}
}

func TestEnsureRoutesFlagAutoTLSWithoutManifest(t *testing.T) {
	app := &ct.App{ID: "app1", Name: "widget"}
	m := &Manifest{
		Routes: []RouteSpec{
			{Type: "http", Domain: "widget.ex.local", Service: "widget"},
		},
	}
	stub := &routeStub{acme: &ct.ACMEConfig{Enabled: true}}
	in := &Installer{Stdout: io.Discard, RouteClient: stub}
	if err := in.ensureRoutes(app, m, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(stub.created) != 1 || !routeHasAutoTLS(stub.created[0]) {
		t.Fatalf("CLI --auto-tls must attach ACME even when the manifest omits auto_tls: %+v", stub.created)
	}
}

func TestEnsureRoutesAutoTLSWhenClusterACMEEnabled(t *testing.T) {
	app := &ct.App{ID: "app1", Name: "widget"}
	m := &Manifest{
		Routes: []RouteSpec{
			{Type: "http", Domain: "widget.ex.local", Service: "widget"},
		},
	}
	stub := &routeStub{acme: &ct.ACMEConfig{Enabled: true}}
	in := &Installer{Stdout: io.Discard, RouteClient: stub}
	if err := in.ensureRoutes(app, m, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(stub.created) != 1 || !routeHasAutoTLS(stub.created[0]) {
		t.Fatalf("enabled cluster ACME must attach TLS on HTTP plugin routes without --auto-tls: %+v", stub.created)
	}

	off := &routeStub{}
	in = &Installer{Stdout: io.Discard, RouteClient: off}
	if err := in.ensureRoutes(app, m, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(off.created) != 1 || routeHasAutoTLS(off.created[0]) {
		t.Fatalf("ACME off and no auto_tls must stay HTTP: %+v", off.created)
	}
}

func TestApplyProvisionedResourcesProvisionsWhenOnlyStaleDatabaseURL(t *testing.T) {
	cluster := map[string]string{
		"DATABASE_URL": "postgres://old-user:old-pass@leader.postgres.discoverd:5432/olddb",
	}
	var provisioned []string
	err := applyProvisionedResources(nil, nil, []string{"postgres"}, cluster, func(name string) (*ct.Resource, error) {
		provisioned = append(provisioned, name)
		return &ct.Resource{
			ProviderID: "uuid-postgres",
			Env: map[string]string{
				"FLYNN_POSTGRES": "postgres",
				"DATABASE_URL":   "postgres://new-user:new-pass@leader.postgres.discoverd:5432/newdb",
			},
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provisioned) != 1 || provisioned[0] != "postgres" {
		t.Fatalf("provisioned=%v", provisioned)
	}
	if cluster["DATABASE_URL"] != "postgres://new-user:new-pass@leader.postgres.discoverd:5432/newdb" {
		t.Fatalf("stale DATABASE_URL must be replaced, got %q", cluster["DATABASE_URL"])
	}
}

func TestApplyProvisionedResourcesSkipsAttachedProviderUUID(t *testing.T) {
	aliases := map[string]string{"postgres": "uuid-postgres", "uuid-postgres": "uuid-postgres"}
	cluster := map[string]string{"DATABASE_URL": "postgres://stale"}
	var provisioned int
	err := applyProvisionedResources([]*ct.Resource{{
		ProviderID: "uuid-postgres",
		Env: map[string]string{
			"FLYNN_POSTGRES": "postgres",
			"DATABASE_URL":   "postgres://live",
		},
	}}, aliases, []string{"postgres"}, cluster, func(string) (*ct.Resource, error) {
		provisioned++
		return nil, fmt.Errorf("must not provision")
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provisioned != 0 {
		t.Fatal("attached postgres must not be provisioned again")
	}
	if cluster["DATABASE_URL"] != "postgres://live" {
		t.Fatalf("resource env must overwrite cluster, got %q", cluster["DATABASE_URL"])
	}
}

func TestRetryableResourceProvision(t *testing.T) {
	unknown := fmt.Errorf("provision postgres: unknown_error: Something went wrong")
	if !retryableResourceProvision(unknown) {
		t.Fatal("collapsed postgres-api unknown_error must retry")
	}
	if retryableResourceProvision(fmt.Errorf("plugin github is not found")) {
		t.Fatal("not found must not retry")
	}
	if retryableResourceProvision(fmt.Errorf("validation: name is required")) {
		t.Fatal("validation must not retry")
	}
	if !retryableResourceProvision(httphelper.JSONError{Code: httphelper.UnknownErrorCode, Retry: true, Message: "dial tcp: i/o timeout"}) {
		t.Fatal("retry JSONError must retry")
	}
}

func TestApplyProvisionedResourcesSkipsFLYNNPostgres(t *testing.T) {
	cluster := map[string]string{}
	var provisioned int
	err := applyProvisionedResources([]*ct.Resource{{
		ProviderID: "some-uuid",
		Env:        map[string]string{"FLYNN_POSTGRES": "postgres", "DATABASE_URL": "postgres://live"},
	}}, nil, []string{"postgres"}, cluster, func(string) (*ct.Resource, error) {
		provisioned++
		return nil, fmt.Errorf("must not provision")
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provisioned != 0 {
		t.Fatal("FLYNN_POSTGRES must count as attached")
	}
}

func TestProviderResourceAttached(t *testing.T) {
	have := map[string]bool{"uuid-1": true}
	aliases := map[string]string{"postgres": "uuid-1"}
	if !providerResourceAttached(have, "postgres", aliases) {
		t.Fatal("name must match provider UUID")
	}
	if providerResourceAttached(have, "redis", aliases) {
		t.Fatal("unrelated provider")
	}
	if !providerResourceAttached(map[string]bool{"postgres": true}, "postgres", nil) {
		t.Fatal("name key")
	}
}

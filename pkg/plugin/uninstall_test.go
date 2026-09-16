package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	host "github.com/flynn/flynn/host/types"
)

type uninstallStub struct {
	apps      []*ct.App
	deleted   []string
	deleteErr error
	listErr   error
	resources map[string][]*ct.Resource
	resErr    error
}

func (s *uninstallStub) AppList() ([]*ct.App, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.apps, nil
}

func (s *uninstallStub) DeleteApp(appID string) (*ct.AppDeletion, error) {
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	s.deleted = append(s.deleted, appID)
	out := s.apps[:0]
	for _, a := range s.apps {
		if a != nil && a.ID != appID {
			out = append(out, a)
		}
	}
	s.apps = out
	return &ct.AppDeletion{AppID: appID}, nil
}

func (s *uninstallStub) ResourceList(providerID string) ([]*ct.Resource, error) {
	if s.resErr != nil {
		return nil, s.resErr
	}
	if s.resources == nil {
		return nil, nil
	}
	return s.resources[providerID], nil
}

func uninstallPluginApp(name string) *ct.App {
	return &ct.App{
		ID:   name + "-id",
		Name: name,
		Meta: map[string]string{
			MetaPlugin:       "true",
			MetaPluginKind:   KindApp,
			MetaPluginSource: name,
		},
	}
}

func TestUninstallDeletesPluginApp(t *testing.T) {
	app := uninstallPluginApp("widget")
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{Stdout: io.Discard, UninstallClient: stub}
	if err := in.Uninstall(UninstallOptions{Name: "widget"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != "widget-id" {
		t.Fatalf("deleted=%v", stub.deleted)
	}
}

func TestUninstallMissingPlugin(t *testing.T) {
	in := &Installer{Stdout: io.Discard, UninstallClient: &uninstallStub{}}
	err := in.Uninstall(UninstallOptions{Name: "missing"})
	if err == nil || !strings.Contains(err.Error(), "not an installed plugin") {
		t.Fatalf("got %v", err)
	}
	if err := in.Uninstall(UninstallOptions{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty: %v", err)
	}
}

func TestUninstallRemovesPluginWebhooks(t *testing.T) {
	app := uninstallPluginApp("dashboard")
	keep := &host.WebhookConfig{ID: "manual-other", URL: "http://other"}
	mine := &host.WebhookConfig{ID: pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn"), URL: "http://dashboard.discoverd/webhooks/flynn"}
	h := &webhookHostStub{id: "host1", listed: []*host.WebhookConfig{keep, mine}}
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{
		Stdout:          io.Discard,
		UninstallClient: stub,
		Hosts:           func() ([]WebhookHost, error) { return []WebhookHost{h}, nil },
	}
	if err := in.Uninstall(UninstallOptions{Name: "dashboard"}); err != nil {
		t.Fatal(err)
	}
	if len(h.removed) != 1 || h.removed[0] != mine.ID {
		t.Fatalf("removed=%v want %s", h.removed, mine.ID)
	}
}

func TestUninstallRefusesProviderWithResources(t *testing.T) {
	app := uninstallPluginApp("redis")
	app.Meta[MetaPluginKind] = KindResourceProvider
	raw, err := json.Marshal(Installed{Name: "redis", Kind: KindResourceProvider, Provider: "redis"})
	if err != nil {
		t.Fatal(err)
	}
	app.Meta[MetaPluginRecord] = string(raw)
	stub := &uninstallStub{
		apps: []*ct.App{app},
		resources: map[string][]*ct.Resource{
			"redis": {{ID: "r1", ProviderID: "redis", Apps: []string{"demo"}}},
		},
	}
	in := &Installer{Stdout: io.Discard, UninstallClient: stub}
	err = in.Uninstall(UninstallOptions{Name: "redis"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("got %v", err)
	}
	if len(stub.deleted) != 0 {
		t.Fatal("must not delete while resources remain")
	}

	if err := in.Uninstall(UninstallOptions{Name: "redis", Force: true}); err != nil {
		t.Fatal(err)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("force deleted=%v", stub.deleted)
	}
}

func TestUninstallAllowsProviderResourcesOnPluginApp(t *testing.T) {
	app := uninstallPluginApp("redis")
	app.Meta[MetaPluginKind] = KindResourceProvider
	raw, err := json.Marshal(Installed{Name: "redis", Kind: KindResourceProvider, Provider: "redis"})
	if err != nil {
		t.Fatal(err)
	}
	app.Meta[MetaPluginRecord] = string(raw)
	stub := &uninstallStub{
		apps: []*ct.App{app},
		resources: map[string][]*ct.Resource{
			"redis": {{ID: "self", ProviderID: "redis", Apps: []string{app.ID}}},
		},
	}
	in := &Installer{Stdout: io.Discard, UninstallClient: stub}
	if err := in.Uninstall(UninstallOptions{Name: "redis"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted=%v", stub.deleted)
	}
}

func TestUninstallRunsHook(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "widget",
		"kind": KindApp,
		"app": map[string]interface{}{
			"name": "widget",
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/widget"}},
			},
		},
		"hooks": map[string]string{"uninstall": "script/uninstall.sh"},
	})
	if err := os.MkdirAll(filepath.Join(dir, "script"), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "uninstalled")
	script := "#!/bin/sh\necho ok > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "script", "uninstall.sh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	app := uninstallPluginApp("widget")
	app.Meta[MetaPluginSource] = dir
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{Stdout: io.Discard, Stderr: io.Discard, UninstallClient: stub}
	if err := in.Uninstall(UninstallOptions{Name: "widget"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("uninstall hook did not run: %v", err)
	}
}

func TestUninstallHookMissingFails(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "widget",
		"kind": KindApp,
		"app": map[string]interface{}{
			"name": "widget",
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/widget"}},
			},
		},
		"hooks": map[string]string{"uninstall": "script/uninstall.sh"},
	})
	app := uninstallPluginApp("widget")
	app.Meta[MetaPluginSource] = dir
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{Stdout: io.Discard, UninstallClient: stub}
	err := in.Uninstall(UninstallOptions{Name: "widget"})
	if err == nil || !strings.Contains(err.Error(), "hook") {
		t.Fatalf("missing declared uninstall hook must fail, got %v", err)
	}
	if len(stub.deleted) != 0 {
		t.Fatal("must not delete after hook failure")
	}
}

func TestPluginWebhookIDPrefix(t *testing.T) {
	id := pluginWebhookID("dashboard", "http://dashboard.discoverd/webhooks/flynn")
	prefix := pluginWebhookIDPrefix("dashboard")
	if !strings.HasPrefix(id, prefix) {
		t.Fatalf("id=%s prefix=%s", id, prefix)
	}
}

func TestUninstallSkipsHookWhenSourceMissing(t *testing.T) {
	app := uninstallPluginApp("widget")
	app.Meta[MetaPluginSource] = "./missing-plugin-checkout"
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{Stdout: io.Discard, UninstallClient: stub}
	if err := in.Uninstall(UninstallOptions{Name: "widget"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted=%v", stub.deleted)
	}
}

func TestUninstallContinuesWhenHostsUnavailable(t *testing.T) {
	app := uninstallPluginApp("widget")
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{
		Stdout:          io.Discard,
		UninstallClient: stub,
		Hosts:           func() ([]WebhookHost, error) { return nil, fmt.Errorf("no hosts") },
	}
	if err := in.Uninstall(UninstallOptions{Name: "widget"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.deleted) != 1 {
		t.Fatalf("deleted=%v", stub.deleted)
	}
}

func TestUninstallFailsWhenWebhookListErrors(t *testing.T) {
	app := uninstallPluginApp("widget")
	h := &webhookHostStub{id: "host1", listErr: fmt.Errorf("boom")}
	stub := &uninstallStub{apps: []*ct.App{app}}
	in := &Installer{
		Stdout:          io.Discard,
		UninstallClient: stub,
		Hosts:           func() ([]WebhookHost, error) { return []WebhookHost{h}, nil },
	}
	err := in.Uninstall(UninstallOptions{Name: "widget"})
	if err == nil || !strings.Contains(err.Error(), "list webhooks") {
		t.Fatalf("got %v", err)
	}
	if len(stub.deleted) != 0 {
		t.Fatal("must not delete after webhook list failure")
	}
}

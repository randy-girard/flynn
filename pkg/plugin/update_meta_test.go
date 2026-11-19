package plugin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bytes"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

// metaFakeClient is the controller subset deploy() touches for an in-place
// plugin update. Unimplemented methods panic through the nil embedded client.
type metaFakeClient struct {
	controller.Client
	meta       map[string]string
	metaWrites []map[string]string
	failMeta   int // fail the Nth (1-based) UpdateAppMeta call; 0 = never
	prevRel    *ct.Release
	prevForm   *ct.Formation
}

func (f *metaFakeClient) GetApp(name string) (*ct.App, error) {
	cp := map[string]string{}
	for k, v := range f.meta {
		cp[k] = v
	}
	return &ct.App{ID: "app-1", Name: name, Meta: cp}, nil
}

func (f *metaFakeClient) UpdateAppMeta(app *ct.App) error {
	cp := map[string]string{}
	for k, v := range app.Meta {
		cp[k] = v
	}
	f.metaWrites = append(f.metaWrites, cp)
	if f.failMeta > 0 && len(f.metaWrites) == f.failMeta {
		return fmt.Errorf("meta write refused")
	}
	f.meta = cp
	return nil
}

func (f *metaFakeClient) AppList() ([]*ct.App, error) { return nil, nil }

func (f *metaFakeClient) GetAppRelease(string) (*ct.Release, error) {
	return f.prevRel, nil
}

func (f *metaFakeClient) GetFormation(string, string) (*ct.Formation, error) {
	return f.prevForm, nil
}

func (f *metaFakeClient) CreateRelease(_ string, r *ct.Release) error {
	r.ID = "rel-new"
	return nil
}

func (f *metaFakeClient) ScaleAppRelease(string, string, ct.ScaleOptions) error { return nil }
func (f *metaFakeClient) SetAppRelease(string, string) error                    { return nil }

func newMetaFixture(t *testing.T, wait string, hooks *Hooks) (*Installer, *metaFakeClient, *Manifest, string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	m := &Manifest{
		Name: "demo",
		Kind: "app",
		Wait: wait,
		App: AppSpec{
			Name:          "demo",
			DeployTimeout: 1,
			Processes:     map[string]ct.ProcessType{"web": {Args: []string{"/bin/demo"}}},
		},
		Hooks: hooks,
	}
	fc := &metaFakeClient{
		meta: map[string]string{
			MetaPlugin:    "true",
			MetaPluginRef: "v1",
		},
		prevRel:  &ct.Release{ID: "rel-old", Env: map[string]string{}},
		prevForm: &ct.Formation{Processes: map[string]int{"web": 1}},
	}
	var log bytes.Buffer
	in := &Installer{Client: fc, Stdout: &log, Stderr: &log}
	return in, fc, m, root, &log
}

func shortWait(t *testing.T) {
	t.Helper()
	op, od, ot := waitHTTPProbeTimeout, waitHTTPRetryDelay, pingTimeout
	waitHTTPProbeTimeout, waitHTTPRetryDelay, pingTimeout = 50*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { waitHTTPProbeTimeout, waitHTTPRetryDelay, pingTimeout = op, od, ot })
}

func runMetaDeploy(in *Installer, m *Manifest, root string) error {
	return in.deploy(root, m, &Resolved{Input: "demo", Ref: "v2"}, InstallOptions{Update: true},
		&ct.Artifact{ID: "art-1"}, map[string]string{})
}

func TestUpdateKeepsPreviousRefWhenReadinessFails(t *testing.T) {
	shortWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	in, fc, m, root, _ := newMetaFixture(t, srv.URL+"/ping", nil)
	in.HTTP = srv.Client()

	err := runMetaDeploy(in, m, root)
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("want readiness error, got %v", err)
	}
	if got := fc.meta[MetaPluginRef]; got != "v1" {
		t.Fatalf("stored ref after failed readiness = %q, want v1 (previous release is the one running)", got)
	}
}

func TestUpdateKeepsPreviousRefWhenHookFails(t *testing.T) {
	shortWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	in, fc, m, root, _ := newMetaFixture(t, srv.URL+"/ping", &Hooks{Upgrade: "up.sh"})
	in.HTTP = srv.Client()
	if err := os.WriteFile(filepath.Join(root, "up.sh"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runMetaDeploy(in, m, root); err == nil || !strings.Contains(err.Error(), "hook") {
		t.Fatalf("want hook error, got %v", err)
	}
	if got := fc.meta[MetaPluginRef]; got != "v1" {
		t.Fatalf("stored ref after failed hook = %q, want v1", got)
	}
}

func TestUpdateRecordsNewRefAfterReadinessAndHooks(t *testing.T) {
	shortWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	in, fc, m, root, _ := newMetaFixture(t, srv.URL+"/ping", &Hooks{Upgrade: "up.sh", Ready: "ready.sh"})
	in.HTTP = srv.Client()
	for _, n := range []string{"up.sh", "ready.sh"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := runMetaDeploy(in, m, root); err != nil {
		t.Fatal(err)
	}
	if got := fc.meta[MetaPluginRef]; got != "v2" {
		t.Fatalf("stored ref after successful update = %q, want v2", got)
	}
	if got := fc.meta[MetaPluginWait]; got != srv.URL+"/ping" {
		t.Fatalf("wait meta = %q", got)
	}
}

func TestUpdateMetaWriteFailureAborts(t *testing.T) {
	shortWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	in, fc, m, root, _ := newMetaFixture(t, srv.URL+"/ping", nil)
	in.HTTP = srv.Client()
	fc.failMeta = 1

	err := runMetaDeploy(in, m, root)
	if err == nil || !strings.Contains(err.Error(), "update plugin meta") {
		t.Fatalf("want meta write error, got %v", err)
	}
	if got := fc.meta[MetaPluginRef]; got != "v1" {
		t.Fatalf("ref = %q", got)
	}
}

func TestUpdateLogsMetaRestoreFailure(t *testing.T) {
	shortWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	in, fc, m, root, log := newMetaFixture(t, srv.URL+"/ping", nil)
	in.HTTP = srv.Client()
	fc.failMeta = 2 // the restore write

	if err := runMetaDeploy(in, m, root); err == nil {
		t.Fatal("want readiness error")
	}
	if !strings.Contains(log.String(), "restore plugin meta") {
		t.Fatalf("restore failure not logged:\n%s", log.String())
	}
}

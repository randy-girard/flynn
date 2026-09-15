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

func TestInstallNoBuildWithoutDist(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	err := (&Installer{}).Install(InstallOptions{Source: dir, NoBuild: true})
	if err == nil || !strings.Contains(err.Error(), "--no-build") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallMissingBuildScript(t *testing.T) {
	dir := t.TempDir()
	writeAppPlugin(t, dir, "widget")
	err := (&Installer{Stderr: io.Discard}).Install(InstallOptions{Source: dir})
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
	}).Install(InstallOptions{Source: dir})
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
	m.Hooks = &Hooks{Install: "hooks/install.sh"}
	if m.installHook() != "hooks/install.sh" {
		t.Fatal(m.installHook())
	}
	in := &Installer{}
	if err := in.runHook(t.TempDir(), m, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := in.runHook(t.TempDir(), m, "hooks/missing.sh", map[string]string{}); err == nil {
		t.Fatal("missing hook must fail")
	}
}

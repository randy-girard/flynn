package ghrelease

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaybeNotifyPrintsWhenNewer(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	t.Setenv(UpdateChannelEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/randy-girard/flynn/releases/latest" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260917.1"})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		Product:        "Flynn CLI",
		UpgradeCommand: "flynn update",
		Repo:           "randy-girard/flynn",
		CheckFile:      filepath.Join(t.TempDir(), "cktime"),
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		MinInterval:    time.Hour,
	})
	got := buf.String()
	if !strings.Contains(got, "A newer Flynn CLI is available (v20260917.1; this is v20260916.0)") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "Run `flynn update` to upgrade") {
		t.Fatalf("got %q", got)
	}
}

func TestMaybeNotifySilentWhenCurrent(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260916.0"})
	}))
	defer srv.Close()
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		CheckFile:      filepath.Join(t.TempDir(), "cktime"),
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
	})
	if buf.Len() != 0 {
		t.Fatalf("got %q", buf.String())
	}
}

func TestMaybeNotifySkippedByEnv(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "1")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
	})
	if hits != 0 || buf.Len() != 0 {
		t.Fatalf("hits=%d buf=%q", hits, buf.String())
	}
}

func TestMaybeNotifyThrottlesGitHubButStillPrints(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	t.Setenv(UpdateChannelEnv, "")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260917.1"})
	}))
	defer srv.Close()
	checkFile := filepath.Join(t.TempDir(), "cktime")
	saveUpdateCheckCache(checkFile, updateCheckCacheFile{Entries: map[string]updateCheckEntry{
		updateCheckKey("randy-girard/flynn", "v20260916.0", ChannelStable): {
			Repo:           "randy-girard/flynn",
			CurrentVersion: "v20260916.0",
			Channel:        ChannelStable,
			Release:        &Release{TagName: "v20260917.1"},
			HasUpdate:      true,
			CheckedAt:      time.Now().UTC(),
		},
	}})
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		CheckFile:      checkFile,
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		MinInterval:    time.Hour,
	})
	if hits != 0 {
		t.Fatalf("hits=%d", hits)
	}
	if !strings.Contains(buf.String(), "A newer Flynn is available (v20260917.1; this is v20260916.0)") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestMaybeNotifyCorruptCacheRefetches(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260917.1"})
	}))
	defer srv.Close()
	checkFile := filepath.Join(t.TempDir(), "cktime")
	if err := os.WriteFile(checkFile, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		CheckFile:      checkFile,
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		MinInterval:    time.Hour,
	})
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
	if !strings.Contains(buf.String(), "v20260917.1") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestMaybeNotifyFailedFetchKeepsCachedLatest(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	t.Setenv(UpdateChannelEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	checkFile := filepath.Join(t.TempDir(), "cktime")
	saveUpdateCheckCache(checkFile, updateCheckCacheFile{Entries: map[string]updateCheckEntry{
		updateCheckKey("randy-girard/flynn", "v20260916.0", ChannelStable): {
			Repo:           "randy-girard/flynn",
			CurrentVersion: "v20260916.0",
			Channel:        ChannelStable,
			Release:        &Release{TagName: "v20260917.1"},
			HasUpdate:      true,
			CheckedAt:      time.Now().UTC().Add(-2 * time.Hour),
		},
	}})
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		CheckFile:      checkFile,
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		MinInterval:    time.Hour,
	})
	if !strings.Contains(buf.String(), "v20260917.1") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestMaybeNotifyPrintsForDevVersion(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260917.2"})
	}))
	defer srv.Close()
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "dev",
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		CheckFile:      filepath.Join(t.TempDir(), "cktime"),
	})
	if !strings.Contains(buf.String(), "this is dev") {
		t.Fatalf("got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "v20260917.2") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestShouldPrintUpdate(t *testing.T) {
	if shouldPrintUpdate("v20260917.2", "v20260917.2") {
		t.Fatal("same tag")
	}
	if !shouldPrintUpdate("v20260917.1", "v20260917.2") {
		t.Fatal("older calver")
	}
	if !shouldPrintUpdate("dev", "v20260917.2") {
		t.Fatal("dev is older than a published tag")
	}
	if !shouldPrintUpdate("v20260917.1-gabcdef", "v20260917.2") {
		t.Fatal("git suffix should not hide an older calver")
	}
	if shouldPrintUpdate("v20260917.1", "") {
		t.Fatal("no latest")
	}
}

func TestMaybeNotifyIgnoresSmokeVersion(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260917.2"})
	}))
	defer srv.Close()
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260917.1-smoke",
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		CheckFile:      filepath.Join(t.TempDir(), "cktime"),
	})
	if hits != 0 || buf.Len() != 0 {
		t.Fatalf("hits=%d buf=%q", hits, buf.String())
	}
}

func TestCompareVersions(t *testing.T) {
	if !CompareVersions("v20260916.0", "v20260917.0") {
		t.Fatal("expected newer")
	}
	if CompareVersions("v20260917.0", "v20260916.0") {
		t.Fatal("expected older")
	}
	if CompareVersions("v20260916.0", "v20260916.0") {
		t.Fatal("expected equal")
	}
}

func TestMaybeNotifySendsGitHubToken(t *testing.T) {
	t.Setenv(SkipUpdateCheckEnv, "")
	t.Setenv("FLYNN_GITHUB_TOKEN", "secret-token")
	t.Setenv("FLYNN_PLUGIN_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(Release{TagName: "v20260917.1"})
	}))
	defer srv.Close()
	var buf bytes.Buffer
	MaybeNotify(NotifyOptions{
		Writer:         &buf,
		CurrentVersion: "v20260916.0",
		HTTPClient:     srv.Client(),
		APIBase:        srv.URL,
		CheckFile:      filepath.Join(t.TempDir(), "cktime"),
	})
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
}

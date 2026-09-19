package ghrelease

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, fetch func(channel string) (*Release, error)) *Client {
	t.Helper()
	t.Setenv(UpdateChannelEnv, "")
	ttl := time.Hour
	c := NewClient("randy-girard/flynn", nil)
	c.CachePath = filepath.Join(t.TempDir(), "update-check-cache.json")
	c.TTL = &ttl
	c.Now = func() time.Time { return time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC) }
	c.fetchLatest = fetch
	return c
}

func TestCheckForUpdateCacheMissFetchesAndStores(t *testing.T) {
	var hits int
	c := testClient(t, func(channel string) (*Release, error) {
		hits++
		if channel != ChannelStable {
			t.Fatalf("channel=%s", channel)
		}
		return &Release{TagName: "v20260918.0"}, nil
	})
	rel, has, err := c.CheckForUpdate("v20260917.0")
	if err != nil || !has || rel == nil || rel.TagName != "v20260918.0" {
		t.Fatalf("rel=%v has=%v err=%v", rel, has, err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
	cache := loadUpdateCheckCache(c.CachePath)
	key := updateCheckKey(c.repo, "v20260917.0", ChannelStable)
	entry, ok := cache.Entries[key]
	if !ok || entry.Release == nil || entry.Release.TagName != "v20260918.0" || !entry.HasUpdate {
		t.Fatalf("cache entry: %#v ok=%v", entry, ok)
	}
}

func TestCheckForUpdateCacheHitSkipsNetwork(t *testing.T) {
	var hits int
	c := testClient(t, func(channel string) (*Release, error) {
		hits++
		return &Release{TagName: "v20260918.0"}, nil
	})
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	if _, has, err := c.CheckForUpdate("v20260917.0"); err != nil || !has {
		t.Fatalf("hit has=%v err=%v", has, err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d want 1", hits)
	}
}

func TestCheckForUpdateExpiredCacheRefetches(t *testing.T) {
	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	var hits int
	c := testClient(t, func(channel string) (*Release, error) {
		hits++
		return &Release{TagName: fmt.Sprintf("v20260918.%d", hits)}, nil
	})
	c.Now = func() time.Time { return now }
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	rel, _, err := c.CheckForUpdate("v20260917.0")
	if err != nil || rel.TagName != "v20260918.2" {
		t.Fatalf("rel=%v err=%v", rel, err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d", hits)
	}
}

func TestCheckForUpdateForceBypassesFreshCache(t *testing.T) {
	var hits int
	c := testClient(t, func(channel string) (*Release, error) {
		hits++
		return &Release{TagName: fmt.Sprintf("v20260918.%d", hits)}, nil
	})
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	rel, _, err := c.CheckForUpdateForce("v20260917.0", true)
	if err != nil || rel.TagName != "v20260918.2" {
		t.Fatalf("rel=%v err=%v", rel, err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d", hits)
	}
}

func TestCheckForUpdateTTLZeroForcesRefresh(t *testing.T) {
	t.Setenv(UpdateCheckTTLEnv, "0")
	var hits int
	c := testClient(t, func(channel string) (*Release, error) {
		hits++
		return &Release{TagName: "v20260918.0"}, nil
	})
	c.TTL = nil // read from env
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d want 2", hits)
	}
}

func TestCheckForUpdateCacheKeyIncludesRepoVersionChannel(t *testing.T) {
	var got []string
	c := testClient(t, func(channel string) (*Release, error) {
		got = append(got, channel)
		return &Release{TagName: "v20260918.0"}, nil
	})
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	c.Channel = ChannelPrerelease
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	c.Channel = ChannelStable
	if _, _, err := c.CheckForUpdate("v20260916.0"); err != nil {
		t.Fatal(err)
	}
	other := *c
	other.repo = "acme/flynn"
	other.fetchLatest = c.fetchLatest
	if _, _, err := other.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("fetches=%d want 4 (distinct keys)", len(got))
	}
	cache := loadUpdateCheckCache(c.CachePath)
	if len(cache.Entries) != 4 {
		t.Fatalf("entries=%d: %#v", len(cache.Entries), cache.Entries)
	}
}

func TestCheckForUpdateStaleOnFetchError(t *testing.T) {
	var fail bool
	c := testClient(t, func(channel string) (*Release, error) {
		if fail {
			return nil, errors.New("network down")
		}
		return &Release{TagName: "v20260918.0"}, nil
	})
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	fail = true
	c.Now = func() time.Time { return time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC) }
	rel, has, err := c.CheckForUpdate("v20260917.0")
	if err != nil || !has || rel.TagName != "v20260918.0" {
		t.Fatalf("stale rel=%v has=%v err=%v", rel, has, err)
	}
}

func TestCheckForUpdateForceFetchErrorDoesNotUseStale(t *testing.T) {
	var fail bool
	c := testClient(t, func(channel string) (*Release, error) {
		if fail {
			return nil, errors.New("network down")
		}
		return &Release{TagName: "v20260918.0"}, nil
	})
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, _, err := c.CheckForUpdateForce("v20260917.0", true); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckForUpdateDoesNotHitHTTPOnFreshCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `{"tag_name":"v20260918.0"}`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("randy-girard/flynn", nil)
	c.SetHTTPClient(srv.Client())
	c.APIBase = srv.URL
	c.CachePath = filepath.Join(t.TempDir(), "cache.json")
	ttl := time.Hour
	c.TTL = &ttl
	if _, _, err := c.CheckForUpdate("v20260917.0"); err != nil {
		t.Fatal(err)
	}
	if _, has, err := c.CheckForUpdate("v20260917.0"); err != nil || !has {
		t.Fatalf("has=%v err=%v", has, err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
}

func TestUpdateCheckTTLParse(t *testing.T) {
	t.Setenv(UpdateCheckTTLEnv, "")
	if UpdateCheckTTL() != DefaultUpdateCheckTTL {
		t.Fatal("default")
	}
	t.Setenv(UpdateCheckTTLEnv, "30m")
	if UpdateCheckTTL() != 30*time.Minute {
		t.Fatal("duration")
	}
	t.Setenv(UpdateCheckTTLEnv, "0")
	if UpdateCheckTTL() != 0 {
		t.Fatal("zero")
	}
	t.Setenv(UpdateCheckTTLEnv, "90")
	if UpdateCheckTTL() != 90*time.Second {
		t.Fatal("seconds")
	}
}

func TestDefaultUpdateCheckCachePath(t *testing.T) {
	t.Setenv(UpdateCheckCacheEnv, "/tmp/custom-cache.json")
	if DefaultUpdateCheckCachePath() != "/tmp/custom-cache.json" {
		t.Fatal(DefaultUpdateCheckCachePath())
	}
	t.Setenv(UpdateCheckCacheEnv, "")
	got := DefaultUpdateCheckCachePath()
	if !strings.HasSuffix(got, filepath.Join(".flynn", "update-check-cache.json")) &&
		!strings.Contains(got, filepath.Join("flynn", "update-check-cache.json")) {
		t.Fatalf("path %q", got)
	}
}

func TestChannelFromVersion(t *testing.T) {
	t.Setenv(UpdateChannelEnv, "")
	if ChannelFromVersion("v20260917.0") != ChannelStable {
		t.Fatal("stable")
	}
	if ChannelFromVersion("v20260917.0-rc.1") != ChannelPrerelease {
		t.Fatal("rc")
	}
	t.Setenv(UpdateChannelEnv, "prerelease")
	if ChannelFromVersion("v20260917.0") != ChannelPrerelease {
		t.Fatal("env")
	}
}

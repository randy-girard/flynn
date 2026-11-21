package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/host/config"
	hosttypes "github.com/randy-girard/flynn/host/types"
)

// CORE-40: POST /host/auth-key on a host without a key.

func core40Host(t *testing.T, authKey, joinToken string) (*Host, string) {
	t.Helper()
	// The in-process path (not systemd) runs in tests; keep the env it sets
	// scoped to this test.
	t.Setenv("INVOCATION_ID", "")
	for _, k := range []string{"FLYNN_HOST_AUTH_KEY", "FLYNN_HOST_JOIN_TOKEN", "DISCOVERD_AUTH_KEY", "AUTH_KEY", "CONTROLLER_KEY", "DISCOVERD", "LD_PRELOAD_CORE40"} {
		t.Setenv(k, "")
	}
	path := filepath.Join(t.TempDir(), "host.json")
	c := config.New()
	if joinToken != "" {
		c.Env = map[string]string{"FLYNN_HOST_JOIN_TOKEN": joinToken}
	}
	if err := c.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	h := &Host{
		authKey:    authKey,
		joinToken:  joinToken,
		configFile: path,
		status:     &hosttypes.HostStatus{ID: "host0"},
		log:        log15.New(),
	}
	h.log.SetHandler(log15.DiscardHandler())
	return h, path
}

func postAuthKey(h *Host, remote, header string, body map[string]interface{}) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/host/auth-key", bytes.NewReader(raw))
	req.RemoteAddr = remote
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set("Auth-Key", header)
	}
	rec := httptest.NewRecorder()
	authKeyServer(h).ServeHTTP(rec, req)
	return rec
}

var authKeyServers sync.Map // *Host -> http.Handler

// authKeyServer is the daemon's middleware chain around ConfigureAuthKey,
// built once per host so the rate limiter keeps its counts.
func authKeyServer(h *Host) http.Handler {
	if v, ok := authKeyServers.Load(h); ok {
		return v.(http.Handler)
	}
	api := &jobAPI{host: h}
	srv := h.rateLimitMiddleware(h.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ConfigureAuthKey(w, r, nil)
	})))
	v, _ := authKeyServers.LoadOrStore(h, srv)
	return v.(http.Handler)
}

func readHostEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	c, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Env == nil {
		return map[string]string{}
	}
	return c.Env
}

func TestConfigureAuthKeyRejectsUnknownEnvKeys(t *testing.T) {
	h, path := core40Host(t, "cluster-secret", "")
	rec := postAuthKey(h, "10.0.0.9:9", "cluster-secret", map[string]interface{}{
		"key": "cluster-secret",
		"env": map[string]string{"LD_PRELOAD_CORE40": "/tmp/evil.so", "DISCOVERD_AUTH_KEY": "disc"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown env key: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	env := readHostEnv(t, path)
	if _, ok := env["LD_PRELOAD_CORE40"]; ok {
		t.Fatal("unknown env key must not reach host.json")
	}
	if _, ok := env["DISCOVERD_AUTH_KEY"]; ok {
		t.Fatal("a rejected request must not write any key")
	}
	if os.Getenv("LD_PRELOAD_CORE40") != "" {
		t.Fatal("unknown env key must not reach the daemon environment")
	}

	// The cluster secrets bootstrap sends are still accepted.
	rec = postAuthKey(h, "10.0.0.9:9", "cluster-secret", map[string]interface{}{
		"key": "cluster-secret",
		"env": map[string]string{
			"DISCOVERD_AUTH_KEY": "disc",
			"AUTH_KEY":           "ctl",
			"CONTROLLER_KEY":     "ctl",
			"DISCOVERD":          "10.0.0.1:1111",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed env: status=%d body=%s", rec.Code, rec.Body.String())
	}
	env = readHostEnv(t, path)
	if env["DISCOVERD_AUTH_KEY"] != "disc" || env["CONTROLLER_KEY"] != "ctl" || env["DISCOVERD"] != "10.0.0.1:1111" {
		t.Fatalf("allowed env not written: %v", env)
	}
}

func TestTOFURequiresJoinToken(t *testing.T) {
	body := func(token string) map[string]interface{} {
		b := map[string]interface{}{"key": "attacker-key"}
		if token != "" {
			b["join_token"] = token
		}
		return b
	}

	t.Run("remote with no join token configured is refused", func(t *testing.T) {
		h, path := core40Host(t, "", "")
		rec := postAuthKey(h, "10.0.0.9:9", "", body(""))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
		}
		if h.currentAuthKey() != "" || readHostEnv(t, path)["FLYNN_HOST_AUTH_KEY"] != "" {
			t.Fatal("a refused claim must not set the key")
		}
	})

	t.Run("remote with wrong or missing join token is refused", func(t *testing.T) {
		h, path := core40Host(t, "", "join-secret")
		for _, tok := range []string{"", "wrong", "join-secre"} {
			rec := postAuthKey(h, "10.0.0.9:9", "", body(tok))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("token %q: status=%d want 401", tok, rec.Code)
			}
		}
		if h.currentAuthKey() != "" || readHostEnv(t, path)["FLYNN_HOST_AUTH_KEY"] != "" {
			t.Fatal("a refused claim must not set the key")
		}
	})

	t.Run("remote with the join token is accepted once", func(t *testing.T) {
		h, path := core40Host(t, "", "join-secret")
		rec := postAuthKey(h, "10.0.0.9:9", "", map[string]interface{}{
			"key": "cluster-secret", "join_token": "join-secret",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if h.currentAuthKey() != "cluster-secret" {
			t.Fatal("key not applied")
		}
		env := readHostEnv(t, path)
		if env["FLYNN_HOST_AUTH_KEY"] != "cluster-secret" {
			t.Fatalf("host.json key not written: %v", env)
		}
		if _, ok := env["FLYNN_HOST_JOIN_TOKEN"]; ok {
			t.Fatal("join token must be removed from host.json once used")
		}
		if h.currentJoinToken() != "" || os.Getenv("FLYNN_HOST_JOIN_TOKEN") != "" {
			t.Fatal("join token must be cleared in the daemon once used")
		}
		// The token cannot be replayed to replace the key.
		rec = postAuthKey(h, "10.0.0.9:9", "", body("join-secret"))
		if rec.Code != http.StatusUnauthorized || h.currentAuthKey() != "cluster-secret" {
			t.Fatalf("replay: status=%d key changed=%v", rec.Code, h.currentAuthKey() != "cluster-secret")
		}
	})

	t.Run("this machine needs no join token", func(t *testing.T) {
		for _, remote := range []string{"127.0.0.1:9", "[::1]:9", "/var/run/flynn-host.sock"} {
			h, _ := core40Host(t, "", "")
			rec := postAuthKey(h, remote, "", map[string]interface{}{"key": "cluster-secret"})
			if rec.Code != http.StatusOK || h.currentAuthKey() != "cluster-secret" {
				t.Fatalf("%s: status=%d body=%s", remote, rec.Code, rec.Body.String())
			}
		}
	})
}

// Two claims racing on a keyless host: exactly one wins.
func TestConfigureAuthKeyConcurrentClaimsSetOneKey(t *testing.T) {
	h, path := core40Host(t, "", "")
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "key-" + string(rune('a'+i))
			codes[i] = postAuthKey(h, "127.0.0.1:9", "", map[string]interface{}{"key": key}).Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d claims succeeded, want 1 (codes %v)", ok, codes)
	}
	if got := readHostEnv(t, path)["FLYNN_HOST_AUTH_KEY"]; got != h.currentAuthKey() {
		t.Fatal("host.json key and in-memory key differ")
	}
}

// The per-IP limiter is installed even when the host starts without a key,
// so remote claims are throttled before and after an in-process key.
func TestRateLimitInstalledWithoutAuthKey(t *testing.T) {
	h, _ := core40Host(t, "", "join-secret")
	limited := false
	for i := 0; i < 120; i++ {
		rec := postAuthKey(h, "198.51.100.9:9", "", map[string]interface{}{"key": "k", "join_token": "guess"})
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("remote claims on a keyless host must be rate limited")
	}
	// Requests from this machine are not limited while the host has no key.
	for i := 0; i < 120; i++ {
		req := httptest.NewRequest("GET", "/host/jobs", nil)
		req.RemoteAddr = "127.0.0.1:9"
		rec := httptest.NewRecorder()
		h.rateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("loopback request %d: status=%d", i, rec.Code)
		}
	}
}

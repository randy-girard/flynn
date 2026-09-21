package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/cluster"
)

func TestRestoreHostEnv(t *testing.T) {
	if got := RestoreHostEnv("", ""); got != nil {
		t.Fatalf("empty backup keys: %#v", got)
	}
	got := RestoreHostEnv("disc-secret", "ctl-secret")
	if got["DISCOVERD_AUTH_KEY"] != "disc-secret" {
		t.Fatalf("DISCOVERD_AUTH_KEY=%q", got["DISCOVERD_AUTH_KEY"])
	}
	if got["AUTH_KEY"] != "ctl-secret" || got["CONTROLLER_KEY"] != "ctl-secret" {
		t.Fatalf("controller keys: %#v", got)
	}
}

func TestConfigureRestoreAuthPushesDiscoverdKey(t *testing.T) {
	var gotEnv map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/auth-key":
			var body struct {
				Key string            `json:"key"`
				Env map[string]string `json:"env"`
			}
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("decode auth-key: %v", err)
			}
			if body.Key == "" {
				t.Error("restore auth POST must include a host API key so the daemon restarts")
			}
			gotEnv = body.Env
			w.WriteHeader(http.StatusOK)
		case "/host/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"node1","auth":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := &State{
		Hosts:       []*cluster.Host{cluster.NewHost("node1", srv.URL, srv.Client(), nil)},
		HostTimeout: 2 * time.Second,
	}
	action := &ConfigureRestoreAuthAction{DiscoverdKey: "disc-secret", ControllerKey: "ctl-secret"}
	if err := action.Run(s); err != nil {
		t.Fatal(err)
	}
	if gotEnv["DISCOVERD_AUTH_KEY"] != "disc-secret" {
		t.Fatalf("host env DISCOVERD_AUTH_KEY=%q, want disc-secret (wait-hosts cannot see an unauthenticated host)", gotEnv["DISCOVERD_AUTH_KEY"])
	}
	if gotEnv["CONTROLLER_KEY"] != "ctl-secret" {
		t.Fatalf("CONTROLLER_KEY=%q", gotEnv["CONTROLLER_KEY"])
	}
	if s.DiscoverdAuthKey() != "disc-secret" {
		t.Fatalf("state discoverd key=%q", s.DiscoverdAuthKey())
	}
}

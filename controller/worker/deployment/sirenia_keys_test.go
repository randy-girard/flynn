package deployment

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/appliancehttp"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// instanceAdminPeer is an isolated database member's admin HTTP: it accepts
// only its APPLIANCE_KEY and records the keys it is shown.
func instanceAdminPeer(t *testing.T, key string) (addr string, seen func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, appliancehttp.RequestKey(r))
		mu.Unlock()
		if !appliancehttp.Authorized(r, key) {
			appliancehttp.Unauthorized(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&sireniaclient.Status{
			Peer:     &state.PeerInfo{Role: state.RolePrimary},
			Database: &sireniaclient.DatabaseInfo{Running: true, ReadWrite: true, XLog: "0/1"},
		})
	}))
	t.Cleanup(srv.Close)
	host, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return net.JoinHostPort(host, strconv.Itoa(port-1)), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

func TestSireniaDeployAuthenticatesWithTheReleaseApplianceKey(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "cluster-secret")
	t.Setenv("AUTH_KEY", "")
	addr, seen := instanceAdminPeer(t, "instance-secret")
	d := &DeployJob{
		oldRelease: &ct.Release{Env: map[string]string{"APPLIANCE_KEY": "instance-secret"}},
		newRelease: &ct.Release{Env: map[string]string{"APPLIANCE_KEY": "instance-secret", "SIRENIA_PROCESS": "mongodb"}},
	}
	if err := d.sireniaClient(addr).WaitForReadWrite(syncTimeout); err != nil {
		t.Fatalf("WaitForReadWrite against an isolated instance: %v", err)
	}
	if msg := sireniaProgressMessage("replication sync", []string{addr}, nil, d.sireniaClient); strings.Contains(msg, "authentication") {
		t.Fatalf("progress message used the wrong key: %q", msg)
	}
	for _, k := range seen() {
		if k != "instance-secret" {
			t.Fatalf("deploy worker sent %q to an isolated instance; it must send only that instance's APPLIANCE_KEY", k)
		}
	}
}

func TestSireniaDeployTriesNewThenOldApplianceKey(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "cluster-secret")
	oldAddr, oldSeen := instanceAdminPeer(t, "old-secret")
	d := &DeployJob{
		oldRelease: &ct.Release{Env: map[string]string{"APPLIANCE_KEY": "old-secret"}},
		newRelease: &ct.Release{Env: map[string]string{"APPLIANCE_KEY": "new-secret"}},
	}
	if _, err := d.sireniaClient(oldAddr).Status(); err != nil {
		t.Fatalf("old-release peer: %v", err)
	}
	for _, k := range oldSeen() {
		if k == "cluster-secret" {
			t.Fatal("the cluster key must never reach an isolated instance")
		}
	}
}

func TestSireniaDeployWithoutApplianceKeyUsesClusterKey(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "cluster-secret")
	addr, _ := instanceAdminPeer(t, "cluster-secret")
	d := &DeployJob{oldRelease: &ct.Release{}, newRelease: &ct.Release{}}
	if _, err := d.sireniaClient(addr).Status(); err != nil {
		t.Fatalf("system appliance (cluster key): %v", err)
	}
}

func TestSireniaDeployRefusedKeyFailsFast(t *testing.T) {
	addr, _ := instanceAdminPeer(t, "instance-secret")
	d := &DeployJob{newRelease: &ct.Release{Env: map[string]string{"APPLIANCE_KEY": "stale-secret"}}}
	err := d.sireniaClient(addr).WaitForReadWrite(syncTimeout)
	if !sireniaclient.IsUnauthorized(err) {
		t.Fatalf("want an unauthorized error at once, got %v", err)
	}
}

// Every admin call of the sirenia deploy goes through d.sireniaClient, so no
// call site can fall back to the cluster key.
func TestSireniaDeployHasNoClusterKeyClients(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`sireniaclient\.NewClient(WithHTTP)?\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := re.FindIndex(src); loc != nil {
			line := strings.Count(string(src[:loc[0]]), "\n") + 1
			if f == "sirenia_keys.go" {
				continue
			}
			t.Errorf("%s:%d builds a sirenia client without the release key; use d.sireniaClient", f, line)
		}
	}
}

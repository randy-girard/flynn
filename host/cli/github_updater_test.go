package cli

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/inconshreveable/log15"
)

func TestImageenvIDs(t *testing.T) {
	ids := imageenvIDs(map[string]*ct.Artifact{
		"redis":         {ID: "redis-id"},
		"slugbuilder":   {ID: "sb-id"},
		"slugrunner":    {ID: "sr-id"},
		"dockerbuilder": {ID: "db-id"},
	})
	if ids.Redis != "redis-id" || ids.SlugBuilder != "sb-id" || ids.SlugRunner != "sr-id" || ids.DockerBuilder != "db-id" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
	if got := imageenvIDs(nil); got.DockerBuilder != "" {
		t.Fatalf("expected empty ids for nil map, got %#v", got)
	}
}

func TestTarballUpdaterSetsRedisApplianceStrategyBeforeDeploy(t *testing.T) {
	src, err := os.ReadFile("github_updater.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	ensure := strings.Index(body, "EnsureRedisApplianceStrategy")
	deploy := strings.Index(body, "deployApp(client, app, redisImage")
	if ensure < 0 || deploy < 0 {
		t.Fatal("tarball updater must call EnsureRedisApplianceStrategy and deploy redis appliances")
	}
	if ensure > deploy {
		t.Fatal("strategy must be persisted before the redis appliance deploy")
	}
}

func TestNormalizeHostname(t *testing.T) {
	if got, want := normalizeHostname("Flynn-Test_Node-1"), "flynntestnode1"; got != want {
		t.Fatalf("normalizeHostname: got %q, want %q", got, want)
	}
}

func TestFindLocalHostPrefersDaemonID(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("daemon", "10.0.0.2:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "flynn-test-node-1", "daemon", map[string]struct{}{"10.0.0.1": {}}, log)
	if h == nil || h.ID() != "daemon" {
		t.Fatalf("expected daemon host, got %#v", h)
	}
}

func TestFindLocalHostMatchesIP(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.2:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "irrelevant", "", map[string]struct{}{"10.0.0.2": {}}, log)
	if h == nil || h.ID() != "h2" {
		t.Fatalf("expected h2, got %#v", h)
	}
}

func TestFindLocalHostMatchesNormalizedHostname(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("flynntestnode1", "10.0.0.1:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1}, "flynn-test-node-1", "", nil, log)
	if h == nil || h.ID() != "flynntestnode1" {
		t.Fatalf("expected flynntestnode1, got %#v", h)
	}
}

func TestFindLocalHostSingleHostFallback(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("only", "10.0.0.1:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1}, "no-match", "", nil, log)
	if h == nil || h.ID() != "only" {
		t.Fatalf("expected only host, got %#v", h)
	}
}

func TestCoordinatorHostIsLocal(t *testing.T) {
	if !coordinatorHostIsLocal("node1", "") {
		t.Fatal("empty daemonID must accept any host")
	}
	if !coordinatorHostIsLocal("node1", "node1") {
		t.Fatal("matching daemonID must be local")
	}
	if coordinatorHostIsLocal("node2", "node1") {
		t.Fatal("a different daemon must not be treated as local")
	}
}

func TestFindLocalHostDoesNotFallbackToPeerAfterRestart(t *testing.T) {
	log := log15.New()
	peer := cluster.NewHost("node2", "192.168.56.21:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{peer}, "node1", "node1", map[string]struct{}{"192.168.56.20": {}}, log)
	if h != nil {
		t.Fatalf("restarting node1 must not treat the only remaining peer as local, got %#v", h)
	}
}

func TestFindLocalHostDoesNotMatchPeerNATIPWhenDaemonIDKnown(t *testing.T) {
	log := log15.New()
	// VirtualBox NAT is 10.0.2.15 on every VM. After a drain the remaining
	// peer can advertise that address while this daemon is still missing
	// from discoverd.
	peer := cluster.NewHost("node2", "10.0.2.15:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{peer}, "node1", "node1", map[string]struct{}{
		"10.0.2.15":     {},
		"192.168.56.20": {},
	}, log)
	if h != nil {
		t.Fatalf("must not treat a peer sharing the NAT IP as local, got %#v", h)
	}
}

func TestFindLocalHostDoesNotMatchPeerHostnameWhenDaemonIDKnown(t *testing.T) {
	log := log15.New()
	peer := cluster.NewHost("node2", "192.168.56.21:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{peer}, "node2", "node1", nil, log)
	if h != nil {
		t.Fatalf("must not match a peer hostname when daemonID is a different host, got %#v", h)
	}
}

func TestFindLocalHostMultipleIPMatchesPicksFirst(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.1:2222", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "no-match", "", map[string]struct{}{"10.0.0.1": {}}, log)
	if h == nil || h.ID() != "h1" {
		t.Fatalf("expected h1, got %#v", h)
	}
}

func TestFindLocalHostNoMatch(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.2:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "no-match", "", map[string]struct{}{"10.0.0.99": {}}, log)
	if h != nil {
		t.Fatalf("expected nil, got %#v", h)
	}
}

// parseHostFromURL drives the coordinator-IP fallback in getCoordinatorIP:
// when discoverd hasn't seen the local daemon re-register yet, we use the
// daemon's own status.URL (e.g. "http://192.168.56.20:1113") to determine
// the cluster-routable IP rather than scanning local interfaces (which
// can return a hypervisor NAT address that peers can't reach).
func TestInstallVersionedBinary(t *testing.T) {
	dir := t.TempDir()

	// Create a minimal gzipped payload
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("test-binary")); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	gzPath := filepath.Join(dir, "test.gz")
	if err := os.WriteFile(gzPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	log := log15.New()
	path, err := installVersionedBinary(gzPath, dir, "flynn-host", "vTEST", log)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "flynn-host.vTEST" {
		t.Fatalf("unexpected versioned path: %s", path)
	}

	linkPath := filepath.Join(dir, "flynn-host")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("symlink missing: %v", err)
	}
	if target != "flynn-host.vTEST" {
		t.Fatalf("symlink target: got %q, want flynn-host.vTEST", target)
	}

	// Second install to a new version should update the symlink without error.
	path2, err := installVersionedBinary(gzPath, dir, "flynn-host", "vTEST2", log)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path2) != "flynn-host.vTEST2" {
		t.Fatalf("unexpected second path: %s", path2)
	}
	target, err = os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if target != "flynn-host.vTEST2" {
		t.Fatalf("symlink not updated: got %q", target)
	}
}

func TestCleanupSourceTarball(t *testing.T) {
	log := log15.New()
	dir := t.TempDir()
	path := filepath.Join(dir, "flynn-vTEST.0.tar.gz")
	if err := os.WriteFile(path, []byte("tarball"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSourceTarball(path, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected tarball to be removed, stat err=%v", err)
	}
}

func TestCleanupSourceTarballRefusesDirectory(t *testing.T) {
	log := log15.New()
	dir := t.TempDir()
	if err := cleanupSourceTarball(dir, log); err == nil {
		t.Fatal("expected error when path is a directory")
	}
}

func TestShouldAbortGitHubUpdate(t *testing.T) {
	const current = "v20260916.2"
	const next = "v20260916.3"
	if !shouldAbortGitHubUpdate(false, false, current, current) {
		t.Fatal("same version without force should abort")
	}
	if shouldAbortGitHubUpdate(true, false, current, current) {
		t.Fatal("--force must continue even when versions match")
	}
	if shouldAbortGitHubUpdate(false, true, current, current) {
		t.Fatal("re-exec mid-update must continue without --force")
	}
	if shouldAbortGitHubUpdate(false, false, current, next) {
		t.Fatal("newer release must update")
	}
}

func TestContinueAfterHostReexec(t *testing.T) {
	t.Setenv(updateReexecEnv, "")
	if continueAfterHostReexec("v20260916.2") {
		t.Fatal("empty env is not a re-exec")
	}
	t.Setenv(updateReexecEnv, "v20260916.2")
	if !continueAfterHostReexec("v20260916.2") {
		t.Fatal("matching FLYNN_UPDATE_REEXEC must continue")
	}
	if continueAfterHostReexec("v20260916.3") {
		t.Fatal("stale re-exec env for a different tag must not skip the version check")
	}
	if continueAfterHostReexec("") {
		t.Fatal("empty target is not a re-exec")
	}
}

func TestGitHubUpdateContinuesAfterReexecBeforeVersionAbort(t *testing.T) {
	src, err := os.ReadFile("github_updater.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	cont := strings.Index(body, "continueAfterHostReexec")
	abort := strings.Index(body, "already on latest version")
	if cont < 0 || abort < 0 {
		t.Fatal("github updater must continue after flynn-host re-exec and still have an already-on-latest abort")
	}
	if cont > abort {
		t.Fatal("re-exec continuation must gate the already-on-latest abort")
	}
	if !strings.Contains(body, "shouldAbortGitHubUpdate(force, continuing, currentVersion, release.TagName)") {
		t.Fatal("runGitHubUpdate must use shouldAbortGitHubUpdate with the re-exec flag")
	}
}

func TestParseHostFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://192.168.56.20:1113", "192.168.56.20"},
		{"http://10.0.0.1:1113", "10.0.0.1"},
		{"http://[fd17:625c:f037:2::1]:1113", "fd17:625c:f037:2::1"},
		{"http://example.host:1113", "example.host"},
		{"http://192.168.56.20", "192.168.56.20"},
		{"", ""},
		{"::not a url::", ""},
	}
	for _, c := range cases {
		if got := parseHostFromURL(c.in); got != c.want {
			t.Errorf("parseHostFromURL(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

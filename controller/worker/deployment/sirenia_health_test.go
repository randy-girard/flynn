package deployment

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/worker/types"
	"github.com/randy-girard/flynn/discoverd/client"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func TestReenteredSireniaDeployDoesNotRollBack(t *testing.T) {
	newPeer := &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": "new-rel"}}
	err := sireniaStartupPeerError("new-rel", newPeer)
	if !errors.Is(err, errSireniaDeployActive) {
		t.Fatalf("startup peer error = %v, want the in-progress deploy sentinel", err)
	}
	if deploymentOutcomeFor(err) != outcomeLeaveRunning {
		t.Fatal("a second worker that sees the new release already up must leave that deploy running")
	}

	if err := sireniaStartupPeerError("new-rel", &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": "old-rel"}}); err != nil {
		t.Fatalf("old release peer = %v, want nil", err)
	}
	if deploymentOutcomeFor(nil) != outcomeComplete {
		t.Fatal("a successful deploy must still be marked complete")
	}
	if deploymentOutcomeFor(worker.ErrStopped) != outcomeLeaveRunning {
		t.Fatal("worker shutdown must still leave the deployment unfinished")
	}
	if deploymentOutcomeFor(ErrSkipRollback{Err: "skip"}) != outcomeSkipRollback {
		t.Fatal("skip-rollback errors must still finish without restoring the old formation")
	}
	if deploymentOutcomeFor(errors.New("boom")) != outcomeRollback {
		t.Fatal("a real deploy failure must still roll back")
	}
}

func TestAsyncBackupSourceFollowsTheLiveTail(t *testing.T) {
	a := &discoverd.Instance{ID: "a"}
	b := &discoverd.Instance{ID: "b"}
	c := &discoverd.Instance{ID: "c"}
	n0 := &discoverd.Instance{ID: "n0"}
	n1 := &discoverd.Instance{ID: "n1"}

	// A single async is the tail, so the replacement follows that peer.
	if got := asyncBackupSource([]*discoverd.Instance{a}, 0, nil); got != a {
		t.Fatalf("single async backup source = %v, want that async", got.ID)
	}
	// A longer chain appends the new job after the current tail. The first
	// replacement follows the tail, not the async it will replace.
	if got := asyncBackupSource([]*discoverd.Instance{a, b, c}, 0, nil); got != c {
		t.Fatalf("first of three backup source = %v, want tail", got.ID)
	}
	// Each later replacement follows the previous new job, which is now the tail.
	if got := asyncBackupSource([]*discoverd.Instance{a, b, c}, 1, n0); got != n0 {
		t.Fatalf("second replacement backup source = %v, want previous replacement", got.ID)
	}
	if got := asyncBackupSource([]*discoverd.Instance{a, b, c}, 2, n1); got != n1 {
		t.Fatalf("third replacement backup source = %v, want previous replacement", got.ID)
	}
}

func TestSireniaStartExcludeKeepsLaterPeers(t *testing.T) {
	started := []*discoverd.Instance{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := sireniaStartExclude(started)
	if len(got) != 3 || got[2].ID != "c" {
		t.Fatalf("exclude has %d peers, want every started peer including the third", len(got))
	}
}

func TestSyncReplacementFollowsTheLastAsync(t *testing.T) {
	first := &discoverd.Instance{ID: "n0"}
	last := &discoverd.Instance{ID: "n2"}
	// One async replacement: that peer is the tail the new sync follows.
	if got := syncReplacementUpstream(first, first); got != first {
		t.Fatalf("single async upstream = %s", got.ID)
	}
	// Several async replacements: the new sync follows the latest tail,
	// which is not the first async that was started.
	if got := syncReplacementUpstream(last, first); got != last {
		t.Fatalf("multi async upstream = %s, want last", got.ID)
	}
}

func TestSireniaClusterDeployReady(t *testing.T) {
	peer := func(n int) *discoverd.Instance {
		return &discoverd.Instance{Meta: map[string]string{"ID": "p"}}
	}
	_ = peer

	threeNode := &state.State{
		Primary: &discoverd.Instance{},
		Sync:    &discoverd.Instance{},
		Async:   []*discoverd.Instance{{}},
	}
	if !sireniaClusterDeployReady(threeNode, 3) {
		t.Fatal("expected 3-node cluster with one async to be deploy-ready")
	}

	noAsync := &state.State{
		Primary: &discoverd.Instance{},
		Sync:    &discoverd.Instance{},
		Deposed: []*discoverd.Instance{{}},
	}
	if sireniaClusterDeployReady(noAsync, 3) {
		t.Fatal("expected cluster without asyncs to be not deploy-ready")
	}

	haMissingAsync := &state.State{
		Primary: &discoverd.Instance{},
		Sync:    &discoverd.Instance{},
	}
	if sireniaClusterDeployReady(haMissingAsync, 3) {
		t.Fatal("expected HA cluster without asyncs to be not deploy-ready")
	}
	if !sireniaClusterShouldWaitForReady(haMissingAsync, 3) {
		t.Fatal("HA cluster missing an async must wait for the scheduler to place it")
	}
	if sireniaClusterShouldWaitForReady(threeNode, 3) {
		t.Fatal("ready 3-node cluster must not wait")
	}
	twoNode := &state.State{Primary: &discoverd.Instance{}, Sync: &discoverd.Instance{}}
	if sireniaClusterShouldWaitForReady(twoNode, 2) {
		t.Fatal("2-process cluster cannot grow an async by waiting")
	}

	singleton := &state.State{Singleton: true}
	if !sireniaClusterDeployReady(singleton, 1) {
		t.Fatal("expected singleton cluster to be deploy-ready")
	}
}

func TestFindSireniaPeerByRelease(t *testing.T) {
	old := &discoverd.Instance{
		ID: "old",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-old",
			"FLYNN_PROCESS_TYPE": "postgres",
		},
	}
	first := &discoverd.Instance{
		ID: "new-1",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-new",
			"FLYNN_PROCESS_TYPE": "postgres",
		},
	}
	second := &discoverd.Instance{
		ID: "new-2",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-new",
			"FLYNN_PROCESS_TYPE": "postgres",
		},
	}
	insts := []*discoverd.Instance{old, first, second}

	got := findSireniaPeerByRelease(insts, "rel-new", "postgres", "POSTGRES_ID")
	if got != first {
		t.Fatalf("want first new peer, got %#v", got)
	}
	got = findSireniaPeerByRelease(insts, "rel-new", "postgres", "POSTGRES_ID", first)
	if got != second {
		t.Fatalf("want second new peer after excluding first, got %#v", got)
	}
	got = findSireniaPeerByRelease(insts, "rel-new", "postgres", "POSTGRES_ID", first, second)
	if got != nil {
		t.Fatalf("want nil when all new peers excluded, got %#v", got)
	}
	if sireniaPeerMatchesRelease(nil, "rel-new", "postgres") {
		t.Fatal("nil instance must not match")
	}

	rereg := &discoverd.Instance{
		ID: "new-1-reregistered",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-new",
			"FLYNN_PROCESS_TYPE": "postgres",
			"POSTGRES_ID":        "peer-1",
		},
	}
	first.Meta["POSTGRES_ID"] = "peer-1"
	second.Meta["POSTGRES_ID"] = "peer-2"
	got = findSireniaPeerByRelease([]*discoverd.Instance{old, rereg, second}, "rel-new", "postgres", "POSTGRES_ID", first)
	if got != second {
		t.Fatalf("want second peer when first re-registered with a new discoverd ID, got %#v", got)
	}
}

func TestSireniaProgressMessageWithoutAddrs(t *testing.T) {
	if got := sireniaProgressMessage("replication sync", nil, nil); got != "replication sync" {
		t.Fatalf("got %q", got)
	}
}

// fakeSireniaPeer serves /status and returns the postgres addr whose
// sirenia API (port+1) is the test server.
func fakeSireniaPeer(t *testing.T, status func() *sireniaclient.Status) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status())
	}))
	t.Cleanup(srv.Close)
	host, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return net.JoinHostPort(host, strconv.Itoa(port-1))
}

func TestSireniaProgressMessagePercent(t *testing.T) {
	up := fakeSireniaPeer(t, func() *sireniaclient.Status {
		return &sireniaclient.Status{
			Peer:     &state.PeerInfo{Role: state.RoleSync},
			Database: &sireniaclient.DatabaseInfo{Running: true, XLog: "0/1000"},
		}
	})
	downXLog := ""
	down := fakeSireniaPeer(t, func() *sireniaclient.Status {
		db := &sireniaclient.DatabaseInfo{Running: downXLog != "", XLog: downXLog}
		if downXLog == "" {
			db.Basebackup = &sireniaclient.BasebackupProgress{DoneBytes: 4 << 30, TotalBytes: 10 << 30, Percent: 40}
		}
		return &sireniaclient.Status{Peer: &state.PeerInfo{ID: "async-1", Role: state.RoleAsync}, Database: db}
	})
	tracker := &sireniaclient.CatchupTracker{}
	got := sireniaProgressMessage("replication sync", []string{up, down}, tracker)
	if !strings.Contains(got, "async: pg_basebackup 40% (4.0 GB of 10.0 GB)") {
		t.Fatalf("basebackup: %q", got)
	}
	downXLog = "0/0800"
	sireniaProgressMessage("replication sync", []string{up, down}, tracker)
	downXLog = "0/0C00"
	got = sireniaProgressMessage("replication sync", []string{up, down}, tracker)
	if !strings.Contains(got, "catching up 50%, 1.0 KB behind") {
		t.Fatalf("catch-up: %q", got)
	}
}

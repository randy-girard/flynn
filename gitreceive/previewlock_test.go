package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/authorizer"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// The keyed leases (EDGE-40) share fakeLocks' map with the app leases; a
// key like "pipeline:shop" never collides with an app id.
func (f *fakeLocks) AcquireKeyedBuildLock(key string, req *ct.BuildLock) (*ct.BuildLock, error) {
	l, err := f.AcquireBuildLock(key, req)
	if l != nil {
		cp := *l
		cp.Key, cp.AppID = key, ""
		return &cp, nil
	}
	return nil, err
}

func (f *fakeLocks) ReleaseKeyedBuildLock(key, holder string) error {
	return f.ReleaseBuildLock(key, holder)
}

func (l *lockedLocks) AcquireKeyedBuildLock(key string, req *ct.BuildLock) (*ct.BuildLock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeLocks.AcquireKeyedBuildLock(key, req)
}

func (l *lockedLocks) ReleaseKeyedBuildLock(key, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeLocks.ReleaseKeyedBuildLock(key, holder)
}

const shopLockKey = "pipeline:shop"

// stubEnsurePreview stands in for `/bin/gitreceived -ensure-preview` in the
// preview hook: the preview app of branch b is shop-b.
func stubEnsurePreview(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitreceived")
	script := "#!/bin/bash\n[[ $1 = -ensure-preview ]] || exit 2\necho \"$2-$3\"\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLYNN_GITRECEIVED", path)
}

type previewRig struct {
	h       *gitHandler
	stub    string
	repo    string
	uploads *int
	locks   *fakeLocks
	mu      *sync.Mutex
	events  *[]string
	remote  string
}

// newPreviewRig serves the preview remote of pipeline "shop" with real git
// and the real preview hook, a stub flynn-receiver and an in-memory lease.
func newPreviewRig(t *testing.T) *previewRig {
	t.Helper()
	requireHookTools(t)
	r := &previewRig{mu: &sync.Mutex{}, events: &[]string{}}
	r.stub = stubReceiver(t)
	stubEnsurePreview(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	r.h = newGitHandler(&fakeApps{apps: map[string]*ct.App{}}, auth)
	r.h.preview = &fakePreview{info: &previewRemoteInfo{Available: true, Pipeline: "shop"}}
	r.locks = &fakeLocks{events: r.events}
	r.h.keyLocks = &lockedLocks{fakeLocks: r.locks, mu: r.mu}
	r.h.lockTTL = time.Second
	r.repo, r.uploads = cacheRepo(t, r.h, r.events, r.mu)
	srv := httptest.NewServer(httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(r.h)))
	t.Cleanup(srv.Close)
	r.remote = strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/_preview/shop.git"
	return r
}

func (r *previewRig) takeEvents() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := strings.Join(*r.events, ",")
	*r.events = nil
	return got
}

func (r *previewRig) cachedRef(t *testing.T, branch string) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	out, _ := exec.Command("git", "-C", r.repo, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Output()
	return strings.TrimSpace(string(out))
}

func newWorkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	return dir
}

// TestSecondPreviewPushToSamePipelineIsRefused (EDGE-40): every branch
// pushed to a pipeline's preview remote shares one repo cache. While one push
// is building, a push of another branch to the same remote is refused with a
// message naming the first, does not deploy, and does not overwrite the
// cache, so the first branch's ref and objects survive. The pipeline lease
// spans the receive RPC's cache download, receive-pack and upload, is
// renewed while the build runs, and is released afterwards.
func TestSecondPreviewPushToSamePipelineIsRefused(t *testing.T) {
	r := newPreviewRig(t)

	base := newWorkRepo(t)
	c0 := commitFile(t, base, "base.txt", "base\n")
	if out, err := gitCmd(base, "push", r.remote, "HEAD:refs/heads/base").CombinedOutput(); err != nil {
		t.Fatalf("first preview push failed: %s\n%s", err, out)
	}
	// the ref advertisement reads the cache unlocked; the receive RPC is
	// locked around its download and upload
	if got := r.takeEvents(); got != "prepare,acquire,prepare,upload,release" {
		t.Errorf("pipeline lease does not span the receive RPC: events = %s", got)
	}

	pusherA, pusherB := newWorkRepo(t), newWorkRepo(t)
	c1 := commitFile(t, pusherA, "a.txt", "a\n")
	c2 := commitFile(t, pusherB, "b.txt", "b\n")
	if err := os.WriteFile(filepath.Join(r.stub, "gate"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	type result struct {
		out []byte
		err error
	}
	doneA := make(chan result, 1)
	go func() {
		out, err := gitCmd(pusherA, "push", r.remote, "HEAD:refs/heads/feat-a").CombinedOutput()
		doneA <- result{out, err}
	}()
	waitForFile(t, filepath.Join(r.stub, "started-"+c1))
	// longer than the TTL: the lease must be renewed, not expire
	time.Sleep(1500 * time.Millisecond)

	doneB := make(chan result, 1)
	go func() {
		out, err := gitCmd(pusherB, "push", r.remote, "HEAD:refs/heads/feat-b").CombinedOutput()
		doneB <- result{out, err}
	}()
	var outB []byte
	var errB error
	select {
	case b := <-doneB:
		outB, errB = b.out, b.err
	case <-time.After(20 * time.Second):
		// B is building alongside A (it waits on the same gate): let both
		// finish, then fail
		os.Remove(filepath.Join(r.stub, "gate"))
		b := <-doneB
		<-doneA
		deploys, _ := os.ReadFile(filepath.Join(r.stub, "deploys"))
		t.Fatalf("second preview push was building while the first was (not refused):\n%s\ndeploys:\n%s\ncached feat-a=%q feat-b=%q", b.out, deploys, r.cachedRef(t, "feat-a"), r.cachedRef(t, "feat-b"))
	}
	if errB == nil {
		t.Fatalf("second preview push succeeded while the first was building:\n%s", outB)
	}
	if !strings.Contains(string(outB), "Push refused: a git push to this pipeline is in progress (started ") || !strings.Contains(string(outB), "push again") {
		t.Fatalf("second preview push was not told why:\n%s", outB)
	}

	if err := os.Remove(filepath.Join(r.stub, "gate")); err != nil {
		t.Fatal(err)
	}
	a := <-doneA
	if a.err != nil {
		t.Fatalf("first preview push failed: %s\n%s", a.err, a.out)
	}
	deploys, err := os.ReadFile(filepath.Join(r.stub, "deploys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(deploys), c2) {
		t.Fatalf("refused preview push deployed:\n%s", deploys)
	}
	if got := r.cachedRef(t, "feat-a"); got != c1 {
		t.Fatalf("cached feat-a = %q, want %s: the refused push overwrote the cache", got, c1)
	}
	if got := r.cachedRef(t, "feat-b"); got != "" {
		t.Fatalf("refused push stored feat-b = %s", got)
	}
	if got := r.cachedRef(t, "base"); got != c0 {
		t.Fatalf("cached base = %q, want %s", got, c0)
	}
	if *r.uploads != 2 {
		t.Fatalf("uploads = %d, want 2 (the refused push must not upload)", *r.uploads)
	}
	if r.locks.held(shopLockKey) {
		t.Fatal("pipeline lease still held after both pushes")
	}

	// once the first push is done, the second goes through and both
	// branches are in the cache
	if out, err := gitCmd(pusherB, "push", r.remote, "HEAD:refs/heads/feat-b").CombinedOutput(); err != nil {
		t.Fatalf("second preview push after the first failed: %s\n%s", err, out)
	}
	if r.cachedRef(t, "feat-a") != c1 || r.cachedRef(t, "feat-b") != c2 {
		t.Fatalf("cache lost a branch: feat-a=%q feat-b=%q", r.cachedRef(t, "feat-a"), r.cachedRef(t, "feat-b"))
	}
	if r.locks.held(shopLockKey) {
		t.Fatal("pipeline lease still held")
	}
}

// TestPreviewPushReleasesLeaseOnFailure: a preview build that fails still
// drops the pipeline lease, so the next push is not refused.
func TestPreviewPushReleasesLeaseOnFailure(t *testing.T) {
	r := newPreviewRig(t)
	fail := filepath.Join(t.TempDir(), "flynn-receiver")
	if err := os.WriteFile(fail, []byte("#!/bin/bash\ncat >/dev/null\necho 'stub build failed'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLYNN_RECEIVER", fail)
	src := newWorkRepo(t)
	commitFile(t, src, "a.txt", "a\n")
	out, err := gitCmd(src, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "stub build failed") {
		t.Fatalf("failing build: err=%v\n%s", err, out)
	}
	if r.locks.held(shopLockKey) {
		t.Fatal("pipeline lease still held after a failed build")
	}
	if got := r.cachedRef(t, "feat"); got != "" {
		t.Fatalf("failed build stored feat = %s", got)
	}

	t.Setenv("FLYNN_RECEIVER", filepath.Join(r.stub, "flynn-receiver"))
	if out, err := gitCmd(src, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput(); err != nil {
		t.Fatalf("push after a failed build: %s\n%s", err, out)
	}
}

// TestPreviewPushReleasesLeaseOnIdleTimeout: a preview push whose pack stops
// arriving is cut off after PUSH_IDLE_TIMEOUT (EDGE-36) and drops the
// pipeline lease without storing the cache.
func TestPreviewPushReleasesLeaseOnIdleTimeout(t *testing.T) {
	r := newPreviewRig(t)
	r.h.idleTimeout = 300 * time.Millisecond
	srv := httptest.NewUnstartedServer(wrapHandler(r.h))
	uploadlimit.ApplyServerTimeouts(srv.Config)
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	zero := strings.Repeat("0", 40)
	cmd := fmt.Sprintf("%s %s refs/heads/feat\x00report-status agent=git/2\n", zero, strings.Repeat("1", 40))
	body := fmt.Sprintf("%04x%s0000PACK\x00\x00\x00\x02\x00\x00\x00\x01", len(cmd)+4, cmd)
	authz := base64.StdEncoding.EncodeToString([]byte("user:cluster-secret"))
	fmt.Fprintf(conn, "POST /_preview/shop.git/git-receive-pack HTTP/1.1\r\nHost: gitreceive\r\nAuthorization: Basic %s\r\nContent-Type: application/x-git-receive-pack-request\r\nContent-Length: 100000\r\n\r\n%s", authz, body)

	waitUntil := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitUntil("the pipeline lease", func() bool { return r.locks.held(shopLockKey) })
	waitUntil("the lease to be released after the idle timeout", func() bool { return !r.locks.held(shopLockKey) })
	if *r.uploads != 0 {
		t.Fatalf("stalled preview push stored the cache (uploads=%d)", *r.uploads)
	}
	if _, err := os.Stat(filepath.Join(r.stub, "deploys")); err == nil {
		t.Fatal("stalled preview push deployed")
	}
}

// TestPreviewPushWithoutLeaseAPIStillDeploys: during a rolling update a new
// gitreceive can talk to a controller without keyed leases. Its 404 lets
// the preview push through as before (EDGE-30's N-1 rule).
func TestPreviewPushWithoutLeaseAPIStillDeploys(t *testing.T) {
	r := newPreviewRig(t)
	r.locks.err = ct.ErrNotFound
	src := newWorkRepo(t)
	rev := commitFile(t, src, "a.txt", "a\n")
	if out, err := gitCmd(src, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput(); err != nil {
		t.Fatalf("preview push failed: %s\n%s", err, out)
	}
	deploys, _ := os.ReadFile(filepath.Join(r.stub, "deploys"))
	if !strings.Contains(string(deploys), rev) || *r.uploads != 1 || r.cachedRef(t, "feat") != rev {
		t.Fatalf("preview push did not deploy/store: deploys=%q uploads=%d", deploys, *r.uploads)
	}
}

// TestPreviewPushRefusedWhenLeaseCannotBeTaken: any other lease error
// refuses the preview push with the reason instead of building unlocked.
func TestPreviewPushRefusedWhenLeaseCannotBeTaken(t *testing.T) {
	r := newPreviewRig(t)
	r.locks.err = errors.New("controller unreachable")
	src := newWorkRepo(t)
	commitFile(t, src, "a.txt", "a\n")
	out, err := gitCmd(src, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput()
	if err == nil {
		t.Fatalf("preview push succeeded without the lease:\n%s", out)
	}
	if !strings.Contains(string(out), "controller unreachable") || !strings.Contains(string(out), "pipeline shop") {
		t.Fatalf("preview push did not say why:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(r.stub, "deploys")); err == nil || *r.uploads != 0 {
		t.Fatalf("refused preview push deployed or uploaded (uploads=%d)", *r.uploads)
	}
}

// TestPreviewPreReceiveRefusesStaleOldRev: the preview hook's form of
// EDGE-09's stale check. A branch whose advertised old rev is no longer its
// tip (another push landed between the advertisement and the receive RPC)
// is refused before a preview app is created or deployed. A refusal from
// gitreceive (FLYNN_PUSH_REFUSED) is printed and nothing is built.
func TestPreviewPreReceiveRefusesStaleOldRev(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	stubEnsurePreview(t)
	repo := t.TempDir()
	if err := initRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := writeRepoHookBytes(repo, previewPrereceiveHook); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "checkout", "-q", "-b", "feat")
	c0 := commitFile(t, repo, "a.txt", "a\n")
	c1 := commitFile(t, repo, "b.txt", "b\n")
	runGit(t, repo, "checkout", "-q", "--detach", c0)
	c2 := commitFile(t, repo, "c.txt", "c\n")
	runGit(t, repo, "checkout", "-q", "feat")

	hook := func(stdin string, env ...string) ([]byte, error) {
		cmd := exec.Command(filepath.Join(repo, ".git", "hooks", "pre-receive"))
		cmd.Dir = filepath.Join(repo, ".git")
		cmd.Env = append(append(os.Environ(), "GIT_DIR="+filepath.Join(repo, ".git"), "RECEIVE_PIPELINE=shop"), env...)
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.CombinedOutput()
	}

	out, err := hook(c0 + " " + c2 + " refs/heads/feat\n")
	if err == nil {
		t.Fatalf("stale preview push passed the hook:\n%s", out)
	}
	if !strings.Contains(string(out), "feat changed since this push started") || !strings.Contains(string(out), "push again") {
		t.Fatalf("stale preview push was not told why:\n%s", out)
	}
	if strings.Contains(string(out), "Creating preview app") {
		t.Fatalf("stale preview push created a preview app:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(stub, "deploys")); err == nil {
		t.Fatal("stale preview push deployed")
	}

	out, err = hook(c1+" "+c2+" refs/heads/feat\n", "FLYNN_PUSH_REFUSED=-----> Push refused: a git push to this pipeline is in progress")
	if err == nil || !strings.Contains(string(out), "to this pipeline is in progress") || strings.Contains(string(out), "Creating preview app") {
		t.Fatalf("refused preview push: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(stub, "deploys")); err == nil {
		t.Fatal("refused preview push deployed")
	}

	// a stale branch refuses the whole push before any branch is deployed
	zero := strings.Repeat("0", len(c0))
	out, err = hook(zero + " " + c2 + " refs/heads/other\n" + c0 + " " + c2 + " refs/heads/feat\n")
	if err == nil || strings.Contains(string(out), "Creating preview app") {
		t.Fatalf("push with one stale branch: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(stub, "deploys")); err == nil {
		t.Fatal("push with one stale branch deployed the other")
	}

	if out, err := hook(c1 + " " + c2 + " refs/heads/feat\n"); err != nil {
		t.Fatalf("current old rev refused: %v\n%s", err, out)
	}
	if out, err := hook(zero + " " + c2 + " refs/heads/new-branch\n"); err != nil {
		t.Fatalf("new branch refused: %v\n%s", err, out)
	}
	if out, err := hook(zero + " " + c2 + " refs/heads/feat\n"); err == nil {
		t.Fatalf("create of an existing branch passed:\n%s", out)
	}
	deploys, _ := os.ReadFile(filepath.Join(stub, "deploys"))
	if strings.Count(string(deploys), c2) != 2 {
		t.Fatalf("deploys = %q, want two of %s", deploys, c2)
	}
}

// TestStalePreviewAdvertisementDoesNotDeploy: the end-to-end form of the
// stale check on a preview remote. B's ref advertisement is served from the
// cache as it was before A updated the same branch; B's receive RPC then
// downloads the cache with A in it. B must be refused without a deploy and
// the cache must keep A.
func TestStalePreviewAdvertisementDoesNotDeploy(t *testing.T) {
	r := newPreviewRig(t)
	fresh := r.h.prepare
	var staleNext string
	r.h.prepare = func(key string) (string, error) {
		r.mu.Lock()
		stale := staleNext
		staleNext = ""
		r.mu.Unlock()
		if stale == "" {
			return fresh(key)
		}
		dest := t.TempDir()
		if out, err := exec.Command("cp", "-a", stale+"/.", dest).CombinedOutput(); err != nil {
			return "", fmt.Errorf("cp: %s: %s", err, out)
		}
		return dest, nil
	}
	base := newWorkRepo(t)
	commitFile(t, base, "base.txt", "base\n")
	if out, err := gitCmd(base, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput(); err != nil {
		t.Fatalf("base preview push failed: %s\n%s", err, out)
	}
	snapshot := filepath.Join(t.TempDir(), "s0")
	if out, err := exec.Command("cp", "-a", r.repo, snapshot).CombinedOutput(); err != nil {
		t.Fatalf("cp: %s: %s", err, out)
	}
	pusherA, pusherB := t.TempDir(), t.TempDir()
	for _, dir := range []string{pusherA, pusherB} {
		runGit(t, dir, "clone", "-q", base, ".")
		runGit(t, dir, "config", "user.email", "t@example.com")
		runGit(t, dir, "config", "user.name", "t")
	}
	c1 := commitFile(t, pusherA, "a.txt", "a\n")
	c2 := commitFile(t, pusherB, "b.txt", "b\n")
	if out, err := gitCmd(pusherA, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput(); err != nil {
		t.Fatalf("preview push A failed: %s\n%s", err, out)
	}
	r.mu.Lock()
	staleNext = snapshot
	r.mu.Unlock()
	out, err := gitCmd(pusherB, "push", r.remote, "HEAD:refs/heads/feat").CombinedOutput()
	if err == nil {
		t.Fatalf("stale preview push B succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "another push landed first") {
		t.Fatalf("stale preview push B was not told why:\n%s", out)
	}
	deploys, _ := os.ReadFile(filepath.Join(r.stub, "deploys"))
	if strings.Contains(string(deploys), c2) {
		t.Fatalf("stale preview push B deployed:\n%s", deploys)
	}
	if got := r.cachedRef(t, "feat"); got != c1 {
		t.Fatalf("cached feat = %s, want %s", got, c1)
	}
}

// TestPreviewPruneWaitsForTheLease: dropping a deleted preview app's branch
// on a ref advertisement stores the cache only when it can take the
// pipeline lease. While a push holds it, the advertisement is still pruned
// but the cache is not uploaded over the running push's.
func TestPreviewPruneWaitsForTheLease(t *testing.T) {
	r := newPreviewRig(t)
	src := newWorkRepo(t)
	rev := commitFile(t, src, "a.txt", "a\n")
	if out, err := gitCmd(src, "push", r.remote, "HEAD:refs/heads/gone").CombinedOutput(); err != nil {
		t.Fatalf("preview push failed: %s\n%s", err, out)
	}
	r.h.preview = &fakePreview{info: &previewRemoteInfo{Available: true, Pipeline: "shop", Branches: []string{}}}
	uploads := *r.uploads

	if _, err := r.locks.AcquireKeyedBuildLock(shopLockKey, &ct.BuildLock{Holder: "running-push", Description: "a git push", TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	advertise := func() string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/_preview/shop.git/info/refs?service=git-receive-pack", nil)
		req.SetBasicAuth("", "cluster-secret")
		wrapHandler(r.h).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("advertisement = %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := advertise(); strings.Contains(body, "refs/heads/gone") {
		t.Fatalf("advertised a deleted preview branch:\n%s", body)
	}
	if *r.uploads != uploads || r.cachedRef(t, "gone") != rev {
		t.Fatalf("prune uploaded while a push held the pipeline lease (uploads %d -> %d)", uploads, *r.uploads)
	}
	if !r.locks.held(shopLockKey) {
		t.Fatal("the running push lost its lease")
	}

	r.locks.ReleaseKeyedBuildLock(shopLockKey, "running-push")
	advertise()
	if *r.uploads != uploads+1 || r.cachedRef(t, "gone") != "" {
		t.Fatalf("prune with the lease free did not store (uploads %d -> %d, gone=%q)", uploads, *r.uploads, r.cachedRef(t, "gone"))
	}
	if r.locks.held(shopLockKey) {
		t.Fatal("the advertisement kept the pipeline lease")
	}
}

// TestPipelineLeaseAgainstOldController: the real controller client against
// a controller from before keyed leases (no /build-locks route: its router
// answers a plain 404) lets the preview push through unlocked; a new
// controller's 409 names the running push and the pipeline.
func TestPipelineLeaseAgainstOldController(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	held := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.Method+" "+req.URL.EscapedPath())
		h := held
		mu.Unlock()
		if h && req.Method == http.MethodPut {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"code":"conflict","message":"held","detail":{"key":"pipeline:shop","holder":"other","description":"a git push","acquired_at":"2026-11-24T10:11:12Z"}}`)
			return
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()
	cc, err := controller.NewClient(srv.URL, "cluster-secret")
	if err != nil {
		t.Fatal(err)
	}
	h := newGitHandler(&fakeApps{}, nil)
	h.keyLocks = cc
	w := httphelper.NewResponseWriter(httptest.NewRecorder(), ctxhelper.NewContextLogger(context.Background(), log15.New()))
	lease, refusal := h.lockPipelinePush(w, "shop")
	if lease != nil || refusal != "" {
		t.Fatalf("old controller: lease=%v refusal=%q, want an unlocked push", lease, refusal)
	}
	mu.Lock()
	if len(paths) != 1 || paths[0] != "PUT /build-locks/pipeline:shop" {
		t.Fatalf("requests = %v", paths)
	}
	held = true
	mu.Unlock()
	_, refusal = h.lockPipelinePush(w, "shop")
	want := "-----> Push refused: a git push to this pipeline is in progress (started 10:11:12 UTC)."
	if !strings.HasPrefix(refusal, want) {
		t.Fatalf("refusal = %q, want prefix %q", refusal, want)
	}
}

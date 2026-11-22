package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// fakeLocks is the controller's build lease in memory.
type fakeLocks struct {
	mu     sync.Mutex
	locks  map[string]*ct.BuildLock
	err    error
	events *[]string
}

func (f *fakeLocks) note(ev string) {
	if f.events != nil {
		*f.events = append(*f.events, ev)
	}
}

func (f *fakeLocks) AcquireBuildLock(appID string, req *ct.BuildLock) (*ct.BuildLock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.locks == nil {
		f.locks = map[string]*ct.BuildLock{}
	}
	now := time.Now()
	exp := now.Add(time.Duration(req.TTLSeconds) * time.Second)
	if cur, ok := f.locks[appID]; ok && cur.ExpiresAt.After(now) && cur.Holder != req.Holder {
		cp := *cur
		return nil, &ct.BuildLockHeldError{Lock: &cp}
	} else if ok && cur.Holder == req.Holder {
		cur.ExpiresAt = &exp
		return cur, nil
	}
	f.note("acquire")
	l := &ct.BuildLock{AppID: appID, Holder: req.Holder, Description: req.Description, AcquiredAt: &now, ExpiresAt: &exp}
	f.locks[appID] = l
	return l, nil
}

func (f *fakeLocks) ReleaseBuildLock(appID, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.locks[appID]; ok && cur.Holder == holder {
		f.note("release")
		delete(f.locks, appID)
	}
	return nil
}

func (f *fakeLocks) held(appID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.locks[appID]
	return ok
}

// requireHookTools skips when the real pre-receive hook cannot run here
// (it pipes through GNU stdbuf and sed -u, which the Linux unit image has).
func requireHookTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"git", "bash", "stdbuf", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if err := exec.Command("sed", "-u", "s/a/a/", "/dev/null").Run(); err != nil {
		t.Skip("sed has no -u")
	}
}

// stubReceiver stands in for /bin/flynn-receiver in the real hook. It logs
// "<rev> <lock holder>" and, while the gate file exists, waits (a build that
// is still running).
func stubReceiver(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/bash
cat >/dev/null
echo "$2 ${FLYNN_BUILD_LOCK_HOLDER:-none}" >> "` + dir + `/deploys"
touch "` + dir + `/started-$2"
while [ -e "` + dir + `/gate" ]; do sleep 0.05; done
echo "stub deployed $2"
`
	path := filepath.Join(dir, "flynn-receiver")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLYNN_RECEIVER", path)
	return dir
}

// cacheRepo is the blobstore's repo cache: prepare hands each request a copy
// with the shipped hook, upload stores the pushed repo back.
func cacheRepo(t *testing.T, h *gitHandler, events *[]string, mu *sync.Mutex) (repo string, uploads *int) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := initRepo(repo); err != nil {
		t.Fatal(err)
	}
	n := 0
	uploads = &n
	h.prepare = func(string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, "prepare")
		dest := t.TempDir()
		if out, err := exec.Command("cp", "-a", repo+"/.", dest).CombinedOutput(); err != nil {
			return "", fmt.Errorf("cp: %s: %s", err, out)
		}
		return dest, nil
	}
	h.upload = func(path, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, "upload")
		n++
		if err := os.RemoveAll(repo); err != nil {
			return err
		}
		if out, err := exec.Command("cp", "-a", path, repo).CombinedOutput(); err != nil {
			return fmt.Errorf("cp: %s: %s", err, out)
		}
		return nil
	}
	return repo, uploads
}

func commitFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", name)
	return strings.TrimSpace(string(mustGit(t, dir, "rev-parse", "HEAD")))
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", path)
}

// TestSecondPushToSameAppWaitsOrIsRefused (EDGE-09): while one push to an app
// is building, a second push to that app is refused with a message naming
// the first, does not deploy, and does not overwrite the repo cache. The
// first push holds the lease from before the repo download until after the
// upload, renews it while it builds, and hands it to flynn-receiver.
func TestSecondPushToSameAppWaitsOrIsRefused(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	var mu sync.Mutex
	var events []string
	locks := &fakeLocks{events: &events}
	// lease events go in the same log as prepare/upload, under one mutex
	h.locks = &lockedLocks{fakeLocks: locks, mu: &mu}
	h.lockTTL = time.Second
	repo, uploads := cacheRepo(t, h, &events, &mu)

	srv := httptest.NewServer(httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(h)))
	defer srv.Close()
	remote := strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/myapp.git"

	base := t.TempDir()
	runGit(t, base, "init", "-q", "-b", "main")
	runGit(t, base, "config", "user.email", "t@example.com")
	runGit(t, base, "config", "user.name", "t")
	c0 := commitFile(t, base, "base.txt", "base\n")
	if out, err := gitCmd(base, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("first push failed: %s\n%s", err, out)
	}
	mu.Lock()
	got := strings.Join(events, ",")
	events = nil
	mu.Unlock()
	// the ref advertisement (GET info/refs) reads the cache unlocked; the
	// receive RPC is locked around its download and upload
	if got != "prepare,acquire,prepare,upload,release" {
		t.Fatalf("lease does not span the receive RPC: events = %s", got)
	}

	clone := func() string {
		dir := t.TempDir()
		runGit(t, dir, "clone", "-q", base, ".")
		runGit(t, dir, "config", "user.email", "t@example.com")
		runGit(t, dir, "config", "user.name", "t")
		return dir
	}
	pusherA, pusherB := clone(), clone()
	c1 := commitFile(t, pusherA, "a.txt", "a\n")
	c2 := commitFile(t, pusherB, "b.txt", "b\n")

	if err := os.WriteFile(filepath.Join(stub, "gate"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	type result struct {
		out []byte
		err error
	}
	doneA := make(chan result, 1)
	go func() {
		out, err := gitCmd(pusherA, "push", remote, "HEAD:refs/heads/main").CombinedOutput()
		doneA <- result{out, err}
	}()
	waitForFile(t, filepath.Join(stub, "started-"+c1))
	// longer than the TTL: the lease must be renewed, not expire
	time.Sleep(1500 * time.Millisecond)

	outB, errB := gitCmd(pusherB, "push", remote, "HEAD:refs/heads/main").CombinedOutput()
	if errB == nil {
		t.Fatalf("second push succeeded while the first was building:\n%s", outB)
	}
	if !strings.Contains(string(outB), "a git push to this app is in progress") || !strings.Contains(string(outB), "push again") {
		t.Fatalf("second push was not told why:\n%s", outB)
	}

	if err := os.Remove(filepath.Join(stub, "gate")); err != nil {
		t.Fatal(err)
	}
	a := <-doneA
	if a.err != nil {
		t.Fatalf("first push failed: %s\n%s", a.err, a.out)
	}

	deploys, err := os.ReadFile(filepath.Join(stub, "deploys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(deploys), c2) {
		t.Fatalf("refused push deployed:\n%s", deploys)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(deploys)), "\n") {
		if strings.HasSuffix(line, " none") {
			t.Fatalf("flynn-receiver was not told the push holds the lease: %q", line)
		}
	}
	head := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "refs/heads/main")))
	if head != c1 {
		t.Fatalf("cached main = %s, want the first push %s (base %s)", head, c1, c0)
	}
	if *uploads != 2 {
		t.Fatalf("uploads = %d, want 2 (the refused push must not upload)", *uploads)
	}
	if locks.held("app-1") {
		t.Fatal("lease still held after both pushes")
	}

	// once the first push is done, the second is an ordinary non-fast-forward
	outB, errB = gitCmd(pusherB, "push", remote, "HEAD:refs/heads/main").CombinedOutput()
	if errB == nil {
		t.Fatalf("stale second push succeeded:\n%s", outB)
	}
	runGit(t, pusherB, "pull", "-q", "--no-rebase", "--no-edit", remote, "main")
	if out, err := gitCmd(pusherB, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("second push after pull failed: %s\n%s", err, out)
	}
}

// lockedLocks records lease events in the same log as prepare/upload.
type lockedLocks struct {
	*fakeLocks
	mu *sync.Mutex
}

func (l *lockedLocks) AcquireBuildLock(appID string, req *ct.BuildLock) (*ct.BuildLock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeLocks.AcquireBuildLock(appID, req)
}

func (l *lockedLocks) ReleaseBuildLock(appID, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeLocks.ReleaseBuildLock(appID, holder)
}

// TestPushWithoutLeaseAPIStillDeploys: during a rolling update gitreceive can
// be newer than the controller. A 404 from the lease API lets the push
// through as before.
func TestPushWithoutLeaseAPIStillDeploys(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	h.locks = &fakeLocks{err: ct.ErrNotFound}
	var mu sync.Mutex
	var events []string
	_, uploads := cacheRepo(t, h, &events, &mu)
	srv := httptest.NewServer(httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(h)))
	defer srv.Close()
	remote := strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/myapp.git"

	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "main")
	runGit(t, src, "config", "user.email", "t@example.com")
	runGit(t, src, "config", "user.name", "t")
	rev := commitFile(t, src, "a.txt", "a\n")
	if out, err := gitCmd(src, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("push failed: %s\n%s", err, out)
	}
	deploys, _ := os.ReadFile(filepath.Join(stub, "deploys"))
	if !strings.Contains(string(deploys), rev) || *uploads != 1 {
		t.Fatalf("push did not deploy/upload: deploys=%q uploads=%d", deploys, *uploads)
	}
}

// TestPushRefusedWhenLeaseCannotBeTaken: any other lease error refuses the
// push with the reason instead of building unlocked.
func TestPushRefusedWhenLeaseCannotBeTaken(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	h.locks = &fakeLocks{err: errors.New("controller unreachable")}
	var mu sync.Mutex
	var events []string
	_, uploads := cacheRepo(t, h, &events, &mu)
	srv := httptest.NewServer(httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(h)))
	defer srv.Close()
	remote := strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/myapp.git"

	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "main")
	runGit(t, src, "config", "user.email", "t@example.com")
	runGit(t, src, "config", "user.name", "t")
	commitFile(t, src, "a.txt", "a\n")
	out, err := gitCmd(src, "push", remote, "HEAD:refs/heads/main").CombinedOutput()
	if err == nil {
		t.Fatalf("push succeeded without the lease:\n%s", out)
	}
	if !strings.Contains(string(out), "controller unreachable") {
		t.Fatalf("push did not say why:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(stub, "deploys")); err == nil || *uploads != 0 {
		t.Fatalf("refused push deployed or uploaded (uploads=%d)", *uploads)
	}
}

// TestPreReceiveRefusesStaleOldRev: smart HTTP advertises refs and receives
// the pack in two requests, each from a fresh repo cache download. A push
// whose advertised main is no longer main (another push landed in between)
// must be refused before it deploys; receive-pack would only reject the ref
// update after the hook had built and released it.
func TestPreReceiveRefusesStaleOldRev(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	repo := t.TempDir()
	if err := initRepo(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "checkout", "-q", "-b", "main")
	c0 := commitFile(t, repo, "a.txt", "a\n")
	c1 := commitFile(t, repo, "b.txt", "b\n")
	runGit(t, repo, "checkout", "-q", "--detach", c0)
	c2 := commitFile(t, repo, "c.txt", "c\n")
	runGit(t, repo, "checkout", "-q", "main")

	hook := func(stdin string) ([]byte, error) {
		cmd := exec.Command(filepath.Join(repo, ".git", "hooks", "pre-receive"))
		cmd.Dir = filepath.Join(repo, ".git")
		cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(repo, ".git"), "RECEIVE_APP=app-1", "RECEIVE_APP_NAME=myapp")
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.CombinedOutput()
	}

	out, err := hook(c0 + " " + c2 + " refs/heads/main\n")
	if err == nil {
		t.Fatalf("stale push passed the hook:\n%s", out)
	}
	if !strings.Contains(string(out), "another push") || !strings.Contains(string(out), "push again") {
		t.Fatalf("stale push was not told why:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(stub, "deploys")); err == nil {
		t.Fatal("stale push deployed")
	}

	if out, err := hook(c1 + " " + c2 + " refs/heads/main\n"); err != nil {
		t.Fatalf("current old rev refused: %v\n%s", err, out)
	}
	zero := strings.Repeat("0", len(c0))
	if out, err := hook(zero + " " + c2 + " refs/heads/master\n"); err != nil {
		t.Fatalf("new branch refused: %v\n%s", err, out)
	}
	if out, err := hook(zero + " " + c2 + " refs/heads/main\n"); err == nil {
		t.Fatalf("create of an existing branch passed:\n%s", out)
	}
	deploys, _ := os.ReadFile(filepath.Join(stub, "deploys"))
	if strings.Count(string(deploys), c2) != 2 {
		t.Fatalf("deploys = %q, want two of %s", deploys, c2)
	}
}

// TestStaleAdvertisementDoesNotDeploy: the end-to-end form of the stale
// check. B's ref advertisement is served from the cache as it was before A
// landed; B's receive RPC then downloads the cache with A in it. B must be
// refused without a deploy, and the cache must keep A.
func TestStaleAdvertisementDoesNotDeploy(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	h.locks = &fakeLocks{}
	var mu sync.Mutex
	var events []string
	repo, uploads := cacheRepo(t, h, &events, &mu)
	fresh := h.prepare
	var staleNext string
	h.prepare = func(key string) (string, error) {
		mu.Lock()
		stale := staleNext
		staleNext = ""
		mu.Unlock()
		if stale == "" {
			return fresh(key)
		}
		dest := t.TempDir()
		if out, err := exec.Command("cp", "-a", stale+"/.", dest).CombinedOutput(); err != nil {
			return "", fmt.Errorf("cp: %s: %s", err, out)
		}
		return dest, nil
	}
	srv := httptest.NewServer(httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(h)))
	defer srv.Close()
	remote := strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/myapp.git"

	base := t.TempDir()
	runGit(t, base, "init", "-q", "-b", "main")
	runGit(t, base, "config", "user.email", "t@example.com")
	runGit(t, base, "config", "user.name", "t")
	commitFile(t, base, "base.txt", "base\n")
	if out, err := gitCmd(base, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("base push failed: %s\n%s", err, out)
	}
	snapshot := filepath.Join(t.TempDir(), "s0")
	if out, err := exec.Command("cp", "-a", repo, snapshot).CombinedOutput(); err != nil {
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
	if out, err := gitCmd(pusherA, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("push A failed: %s\n%s", err, out)
	}
	mu.Lock()
	staleNext = snapshot
	mu.Unlock()
	out, err := gitCmd(pusherB, "push", remote, "HEAD:refs/heads/main").CombinedOutput()
	if err == nil {
		t.Fatalf("stale push B succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "another push landed first") {
		t.Fatalf("stale push B was not told why:\n%s", out)
	}
	deploys, _ := os.ReadFile(filepath.Join(stub, "deploys"))
	if strings.Contains(string(deploys), c2) {
		t.Fatalf("stale push B deployed:\n%s", deploys)
	}
	if head := strings.TrimSpace(string(mustGit(t, repo, "rev-parse", "refs/heads/main"))); head != c1 {
		t.Fatalf("cached main = %s, want %s", head, c1)
	}
	// receive-pack exits 0 after a declined hook, so the stale RPC stores
	// the cache it downloaded, under the lease: A's refs and objects stay.
	if *uploads != 3 {
		t.Fatalf("uploads = %d, want 3", *uploads)
	}
}

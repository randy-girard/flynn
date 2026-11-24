package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// receivePackRunning reports whether a git receive-pack for repo is still
// running (Linux /proc only; ok is false where it cannot tell).
func receivePackRunning(repo string) (running, ok bool) {
	procs, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil || len(procs) == 0 {
		return false, false
	}
	for _, p := range procs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		args := strings.Split(string(b), "\x00")
		hasRepo, isGit := false, false
		for _, a := range args {
			if a == repo {
				hasRepo = true
			}
			if a == "receive-pack" || strings.HasSuffix(a, "git-receive-pack") {
				isGit = true
			}
		}
		if hasRepo && isGit {
			return true, true
		}
	}
	return false, true
}

// TestPostRPCStalledBodyTimesOut (EDGE-36): a push whose pack stops arriving
// is answered within PUSH_IDLE_TIMEOUT and its connection closed; git
// receive-pack is stopped, the pre-receive hook never runs, the repo cache
// is not stored, the temp repo is removed and the app's build lease is
// released, so the next push goes through. A client that asked for
// report-status gets a 200 receive-pack result rejecting the ref (EDGE-37),
// one that did not gets 408.
func TestPostRPCStalledBodyTimesOut(t *testing.T) {
	t.Run("report-status", func(t *testing.T) { testPostRPCStalledBody(t, "report-status side-band-64k agent=git/2") })
	t.Run("no report-status", func(t *testing.T) { testPostRPCStalledBody(t, "agent=git/2") })
}

func testPostRPCStalledBody(t *testing.T, caps string) {
	requireHookTools(t)
	stub := stubReceiver(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	locks := &fakeLocks{}
	h.locks = locks
	h.idleTimeout = 300 * time.Millisecond
	var mu sync.Mutex
	var events []string
	_, uploads := cacheRepo(t, h, &events, &mu)
	prepare := h.prepare
	var repos []string
	h.prepare = func(key string) (string, error) {
		dir, err := prepare(key)
		mu.Lock()
		repos = append(repos, dir)
		mu.Unlock()
		return dir, err
	}

	srv := httptest.NewUnstartedServer(wrapHandler(h))
	uploadlimit.ApplyServerTimeouts(srv.Config)
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	zero := strings.Repeat("0", 40)
	cmd := fmt.Sprintf("%s %s refs/heads/main\x00%s\n", zero, strings.Repeat("1", 40), caps)
	body := fmt.Sprintf("%04x%s0000PACK\x00\x00\x00\x02\x00\x00\x00\x01", len(cmd)+4, cmd)
	authz := base64.StdEncoding.EncodeToString([]byte("user:cluster-secret"))
	fmt.Fprintf(conn, "POST /myapp.git/git-receive-pack HTTP/1.1\r\nHost: gitreceive\r\nAuthorization: Basic %s\r\nContent-Type: application/x-git-receive-pack-request\r\nContent-Length: 100000\r\n\r\n%s", authz, body)

	// the push is under way: lease held, receive-pack waiting for the pack
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the lease", func() bool { return locks.held("app-1") })
	mu.Lock()
	repo := repos[len(repos)-1]
	mu.Unlock()

	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no answer to the stalled push after %s: %s", time.Since(start).Round(time.Millisecond), err)
	}
	msg, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.HasPrefix(caps, "report-status") {
		if res.StatusCode != http.StatusOK || !strings.Contains(string(msg), "ng refs/heads/main no data for PUSH_IDLE_TIMEOUT") {
			t.Fatalf("stalled push = %d %q, want 200 rejecting refs/heads/main", res.StatusCode, msg)
		}
	} else if res.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("stalled push = %d %q, want 408", res.StatusCode, msg)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the stalled push was cut off only after %s", d)
	}
	if !strings.Contains(string(msg), "PUSH_IDLE_TIMEOUT") {
		t.Fatalf("error does not name the setting: %q", msg)
	}
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the connection of a timed out push is still open")
	}

	waitFor("the lease release", func() bool { return !locks.held("app-1") })
	waitFor("the temp repo removal", func() bool { _, err := os.Stat(repo); return os.IsNotExist(err) })
	if running, ok := receivePackRunning(repo); ok {
		waitFor("receive-pack to stop", func() bool { r, _ := receivePackRunning(repo); return !r })
	} else if running {
		t.Fatal("receive-pack still running")
	}
	if _, err := os.Stat(filepath.Join(stub, "deploys")); err == nil {
		t.Fatal("the hook ran for a stalled push")
	}
	if *uploads != 0 {
		t.Fatalf("a stalled push stored the repo cache (%d uploads)", *uploads)
	}

	// the next push takes the lease and deploys
	remote := strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/myapp.git"
	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "main")
	runGit(t, src, "config", "user.email", "t@example.com")
	runGit(t, src, "config", "user.name", "t")
	rev := commitFile(t, src, "a.txt", "a\n")
	if out, err := gitCmd(src, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("push after the stalled one failed: %s\n%s", err, out)
	}
	deploys, _ := os.ReadFile(filepath.Join(stub, "deploys"))
	if !strings.Contains(string(deploys), rev) || *uploads != 1 {
		t.Fatalf("push after the stalled one did not deploy/upload: deploys=%q uploads=%d", deploys, *uploads)
	}
}

// A push whose hook (the build) runs far longer than the idle timeout is not
// cut off: the deadline ends with the pack.
func TestPushWithSlowBuildOutlivesIdleTimeout(t *testing.T) {
	requireHookTools(t)
	stub := stubReceiver(t)
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	h.locks = &fakeLocks{}
	h.idleTimeout = 200 * time.Millisecond
	var mu sync.Mutex
	var events []string
	_, uploads := cacheRepo(t, h, &events, &mu)
	srv := httptest.NewUnstartedServer(wrapHandler(h))
	uploadlimit.ApplyServerTimeouts(srv.Config)
	srv.Start()
	defer srv.Close()
	remote := strings.Replace(srv.URL, "http://", "http://user:cluster-secret@", 1) + "/myapp.git"

	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "main")
	runGit(t, src, "config", "user.email", "t@example.com")
	runGit(t, src, "config", "user.name", "t")
	rev := commitFile(t, src, "a.txt", "a\n")
	gate := filepath.Join(stub, "gate")
	if err := os.WriteFile(gate, nil, 0644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(time.Second)
		os.Remove(gate)
	}()
	if out, err := gitCmd(src, "push", remote, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("push with a slow build failed: %s\n%s", err, out)
	}
	deploys, _ := os.ReadFile(filepath.Join(stub, "deploys"))
	if !strings.Contains(string(deploys), rev) || *uploads != 1 {
		t.Fatalf("slow build did not deploy/upload: deploys=%q uploads=%d", deploys, *uploads)
	}
}

func TestPushIdleTimeoutSetting(t *testing.T) {
	t.Setenv("PUSH_IDLE_TIMEOUT", "")
	h := newGitHandler(nil, nil)
	if err := h.configure(); err != nil {
		t.Fatal(err)
	}
	if h.idleTimeout != defaultPushIdleTimeout {
		t.Fatalf("default = %s", h.idleTimeout)
	}
	t.Setenv("PUSH_IDLE_TIMEOUT", "off")
	if err := h.configure(); err != nil || h.idleTimeout != 0 {
		t.Fatalf("off = %s, %v", h.idleTimeout, err)
	}
	t.Setenv("PUSH_IDLE_TIMEOUT", "2m")
	if err := h.configure(); err != nil || h.idleTimeout != 2*time.Minute {
		t.Fatalf("2m = %s, %v", h.idleTimeout, err)
	}
	t.Setenv("PUSH_IDLE_TIMEOUT", "soon")
	if err := h.configure(); err == nil || !strings.Contains(err.Error(), "PUSH_IDLE_TIMEOUT") {
		t.Fatalf("bad setting error = %v", err)
	}
}

package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// refusalSetup is a gitreceive handler with the real pre-receive hook, a stub
// flynn-receiver, an in-memory build lease and repo cache, served over HTTP.
type refusalSetup struct {
	h       *gitHandler
	srv     *httptest.Server
	stub    string
	cache   string
	uploads *int
	locks   *fakeLocks
	mu      sync.Mutex
}

func newRefusalSetup(t *testing.T) *refusalSetup {
	t.Helper()
	requireHookTools(t)
	s := &refusalSetup{stub: stubReceiver(t)}
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	s.h = newGitHandler(&fakeApps{apps: map[string]*ct.App{"myapp": {ID: "app-1", Name: "myapp"}}}, auth)
	s.locks = &fakeLocks{}
	s.h.locks = s.locks
	var events []string
	s.cache, s.uploads = cacheRepo(t, s.h, &events, &s.mu)
	s.srv = httptest.NewUnstartedServer(wrapHandler(s.h))
	uploadlimit.ApplyServerTimeouts(s.srv.Config)
	s.srv.Start()
	t.Cleanup(s.srv.Close)
	return s
}

func (s *refusalSetup) remote(addr string) string {
	return "http://user:cluster-secret@" + addr + "/myapp.git"
}

// source is a client repo whose first commit is already pushed (and
// deployed), so later pushes send only their new objects.
func (s *refusalSetup) source(t *testing.T) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	base = commitFile(t, dir, "small.txt", "small\n")
	if out, err := gitCmd(dir, "push", s.remote(s.srv.Listener.Addr().String()), "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("first push failed: %s\n%s", err, out)
	}
	return dir, base
}

// commitRandom commits n random (incompressible) bytes, so the pack of the
// next push is a little over n bytes.
func commitRandom(t *testing.T, dir string, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("big-%d.bin", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", name)
	return strings.TrimSpace(string(mustGit(t, dir, "rev-parse", "HEAD")))
}

// assertRefused checks that nothing of a refused push landed: the cached
// branch did not move, the hook built nothing for rev, the lease is free.
func (s *refusalSetup) assertRefused(t *testing.T, base, rev string, uploadsBefore int) {
	t.Helper()
	if head := strings.TrimSpace(string(mustGit(t, s.cache, "rev-parse", "refs/heads/main"))); head != base {
		t.Fatalf("main moved to %s after the refused push, want %s", head, base)
	}
	deploys, _ := os.ReadFile(filepath.Join(s.stub, "deploys"))
	if strings.Contains(string(deploys), rev) {
		t.Fatalf("the refused push was built: %q", deploys)
	}
	for deadline := time.Now().Add(5 * time.Second); s.locks.held("app-1"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the build lease is still held after the refused push")
		}
	}
	// neither the refused pack nor the probe git sends before a chunked
	// one stores the repo cache
	s.mu.Lock()
	defer s.mu.Unlock()
	if *s.uploads != uploadsBefore {
		t.Fatalf("the refused push stored the repo cache %d times", *s.uploads-uploadsBefore)
	}
}

// TestOversizePushReportsLimitToGit (EDGE-37): a push over MAX_PUSH_SIZE
// fails with a message git prints, naming the limit and the setting, not a
// bare "RPC failed; HTTP 413". Git shows no body of a failed RPC, so the
// refusal is a 200 report-status that rejects every ref, with the reason on
// side-band 2 ("remote: ..."). A pack sent with its length is answered
// before it is read; a chunked pack (git sends any pack over
// http.postBuffer, 1 MiB, chunked) is read and discarded to its end, up to
// MAX_PUSH_DRAIN past the limit, because git only reads the answer once it
// has sent the whole pack. Past that the push gets the bare 413.
func TestOversizePushReportsLimitToGit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drain int64
		args  []string
		want  []string
		bare  bool
	}{
		// 2 MiB is over http.postBuffer, so git sends it chunked
		{name: "chunked", drain: defaultMaxPushDrain},
		{name: "with length", drain: defaultMaxPushDrain, args: []string{"-c", "http.postBuffer=16777216"}},
		// a pack with a length is answered without reading it, so the
		// drain bound does not matter
		{name: "with length, no drain", drain: 0, args: []string{"-c", "http.postBuffer=16777216"}},
		{name: "chunked past the drain bound", drain: 512 << 10, bare: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRefusalSetup(t)
			s.h.maxPushSize = 256 << 10
			s.h.maxPushDrain = tc.drain
			src, base := s.source(t)
			s.mu.Lock()
			before := *s.uploads
			s.mu.Unlock()
			rev := commitRandom(t, src, 2<<20)
			args := append(append([]string{}, tc.args...), "push", s.remote(s.srv.Listener.Addr().String()), "HEAD:refs/heads/main")
			out, err := gitCmd(src, args...).CombinedOutput()
			if err == nil {
				t.Fatalf("oversize push succeeded:\n%s", out)
			}
			t.Logf("git push output:\n%s", out)
			if tc.bare {
				if !strings.Contains(string(out), "HTTP 413") {
					t.Fatalf("push past the drain bound did not get the 413:\n%s", out)
				}
			} else {
				for _, want := range []string{
					"remote: -----> Push refused: it is larger than the gitreceive limit of 256 KiB (MAX_PUSH_SIZE)",
					"flynn -a gitreceive env:set MAX_PUSH_SIZE=<size>",
					"[remote rejected] HEAD -> main (push larger than MAX_PUSH_SIZE (256 KiB))",
				} {
					if !strings.Contains(string(out), want) {
						t.Fatalf("git output lacks %q:\n%s", want, out)
					}
				}
				if strings.Contains(string(out), "RPC failed") {
					t.Fatalf("git still reports a failed RPC:\n%s", out)
				}
			}
			s.assertRefused(t, base, rev, before)

			// a push under the limit still deploys
			runGit(t, src, "reset", "-q", "--hard", base)
			next := commitFile(t, src, "next.txt", "next\n")
			if out, err := gitCmd(src, "push", s.remote(s.srv.Listener.Addr().String()), "HEAD:refs/heads/main").CombinedOutput(); err != nil {
				t.Fatalf("push after the refused one failed: %s\n%s", err, out)
			}
			deploys, _ := os.ReadFile(filepath.Join(s.stub, "deploys"))
			if !strings.Contains(string(deploys), next) {
				t.Fatalf("push after the refused one did not deploy: %q", deploys)
			}
		})
	}
}

// stallProxy forwards TCP connections to target. The first connection that
// has carried more than after bytes from the client stops reading from it,
// as a client whose network stalls mid-pack; answers still reach the
// client. Other connections pass through.
type stallProxy struct {
	ln      net.Listener
	target  string
	after   int64
	stalled atomic.Bool
	sent    atomic.Int64
}

func newStallProxy(t *testing.T, target string, after int64) *stallProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &stallProxy{ln: ln, target: target, after: after}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *stallProxy) serve(client net.Conn) {
	defer client.Close()
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		// server to client: everything, then a half close so the client
		// reads the whole answer before the connection goes away
		io.Copy(client, server)
		client.(*net.TCPConn).CloseWrite()
		time.Sleep(500 * time.Millisecond)
		close(done)
	}()
	buf := make([]byte, 32<<10)
	stalling := false
	for {
		if !stalling && p.sent.Load() > p.after && p.stalled.CompareAndSwap(false, true) {
			stalling = true
		}
		if stalling {
			break
		}
		n, err := client.Read(buf)
		if n > 0 {
			p.sent.Add(int64(n))
			if _, werr := server.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			server.(*net.TCPConn).CloseWrite()
			break
		}
	}
	<-done
}

// TestStalledPushReportsIdleTimeoutToGit (EDGE-37): a push whose pack stops
// arriving for PUSH_IDLE_TIMEOUT fails with a message git prints, naming the
// timeout and the setting, not a bare "RPC failed; HTTP 408".
func TestStalledPushReportsIdleTimeoutToGit(t *testing.T) {
	s := newRefusalSetup(t)
	s.h.idleTimeout = 500 * time.Millisecond
	src, base := s.source(t)
	s.mu.Lock()
	before := *s.uploads
	s.mu.Unlock()
	rev := commitRandom(t, src, 4<<20)
	// stall a little into the pack: past the advertisement and the probe
	p := newStallProxy(t, s.srv.Listener.Addr().String(), 1<<20)
	cmd := gitCmd(src, "push", s.remote(p.ln.Addr().String()), "HEAD:refs/heads/main")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err == nil {
			t.Fatalf("stalled push succeeded:\n%s", out.String())
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		<-waitErr
		t.Fatalf("git still waiting 30s after its push stalled:\n%s", out.String())
	}
	t.Logf("git push output:\n%s", out.String())
	if !p.stalled.Load() {
		t.Fatal("the proxy never stalled the push")
	}
	for _, want := range []string{
		"remote: -----> Push refused: no data arrived for 500ms, the gitreceive upload idle timeout (PUSH_IDLE_TIMEOUT)",
		"flynn -a gitreceive env:set PUSH_IDLE_TIMEOUT=<duration>",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("git output lacks %q:\n%s", want, out.String())
		}
	}
	s.assertRefused(t, base, rev, before)
}

func TestPushDrainSetting(t *testing.T) {
	t.Setenv("MAX_PUSH_DRAIN", "")
	h := newGitHandler(nil, nil)
	if err := h.configure(); err != nil {
		t.Fatal(err)
	}
	if h.maxPushDrain != defaultMaxPushDrain {
		t.Fatalf("default = %d", h.maxPushDrain)
	}
	t.Setenv("MAX_PUSH_DRAIN", "0")
	if err := h.configure(); err != nil || h.maxPushDrain != 0 {
		t.Fatalf("0 = %d, %v", h.maxPushDrain, err)
	}
	t.Setenv("MAX_PUSH_DRAIN", "64m")
	if err := h.configure(); err != nil || h.maxPushDrain != 64<<20 {
		t.Fatalf("64m = %d, %v", h.maxPushDrain, err)
	}
	t.Setenv("MAX_PUSH_DRAIN", "lots")
	if err := h.configure(); err == nil || !strings.Contains(err.Error(), "MAX_PUSH_DRAIN") {
		t.Fatalf("bad setting error = %v", err)
	}
}

func TestReadPushCommands(t *testing.T) {
	z, a, b := strings.Repeat("0", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)
	var req bytes.Buffer
	pktLine(&req, "shallow "+a+"\n")
	pktLine(&req, z+" "+a+" refs/heads/main\x00report-status-v2 side-band-64k agent=git/2\n")
	pktLine(&req, a+" "+b+" refs/tags/v1\n")
	pktFlush(&req)
	req.WriteString("PACK rest of the pack")
	want := req.String()

	cmds, body := readPushCommands(bytes.NewReader(req.Bytes()))
	if got := strings.Join(cmds.refs, ","); got != "refs/heads/main,refs/tags/v1" {
		t.Fatalf("refs = %q", got)
	}
	if !cmds.reportStatus || cmds.sideBand != 65520 {
		t.Fatalf("report-status %v side-band %d", cmds.reportStatus, cmds.sideBand)
	}
	rest, _ := io.ReadAll(body)
	if string(rest) != want {
		t.Fatalf("git would read %q, want %q", rest, want)
	}

	// not a push request (the probe, garbage): nothing to report on, and
	// git still gets every byte
	for _, in := range []string{"0000", "PACKxxxxxxxx", "00", "0000PACK"} {
		cmds, body := readPushCommands(strings.NewReader(in))
		if cmds.canReport() {
			t.Fatalf("%q: can report", in)
		}
		if cmds.isProbe() != strings.HasPrefix(in, "0000") {
			t.Fatalf("%q: probe = %v", in, cmds.isProbe())
		}
		if cmds.notGit != strings.HasPrefix(in, "PACK") {
			t.Fatalf("%q: notGit = %v", in, cmds.notGit)
		}
		if rest, _ := io.ReadAll(body); string(rest) != in {
			t.Fatalf("%q: git would read %q", in, rest)
		}
	}

	// side-band without -64k, no report-status
	req.Reset()
	pktLine(&req, z+" "+a+" refs/heads/main\x00side-band\n")
	pktFlush(&req)
	cmds, _ = readPushCommands(bytes.NewReader(req.Bytes()))
	if cmds.reportStatus || cmds.sideBand != 1000 || cmds.canReport() {
		t.Fatalf("side-band only: %+v", cmds)
	}
}

// TestRefusalReportFormat: the answer is side-band 2 lines, then the
// report-status on band 1 split to fit the band, then a flush; without
// side-band only the report-status.
func TestRefusalReportFormat(t *testing.T) {
	r := refusal{lines: []string{"first line", "second line"}, status: "too big"}
	var got bytes.Buffer
	writeRefusal(&got, &pushCommands{refs: []string{"refs/heads/main"}, reportStatus: true, sideBand: 65520}, r)
	var want bytes.Buffer
	band := func(ch byte, s string) { fmt.Fprintf(&want, "%04x%c%s", len(s)+5, ch, s) }
	band(2, "first line\n")
	band(2, "second line\n")
	band(1, "0013unpack too big\n001fng refs/heads/main too big\n0000")
	want.WriteString("0000")
	if got.String() != want.String() {
		t.Fatalf("side-band answer\n got %q\nwant %q", got.String(), want.String())
	}

	got.Reset()
	writeRefusal(&got, &pushCommands{refs: []string{"refs/heads/main"}, reportStatus: true}, r)
	if s := got.String(); s != "0013unpack too big\n001fng refs/heads/main too big\n0000" {
		t.Fatalf("plain answer %q", s)
	}

	// many refs: no band packet over the side-band packet size
	var refs []string
	for i := 0; i < 50; i++ {
		refs = append(refs, fmt.Sprintf("refs/heads/branch-%03d", i))
	}
	got.Reset()
	writeRefusal(&got, &pushCommands{refs: refs, reportStatus: true, sideBand: 1000}, r)
	data := got.Bytes()
	var status bytes.Buffer
	for len(data) >= 4 {
		var n int
		fmt.Sscanf(string(data[:4]), "%04x", &n)
		if n == 0 {
			data = data[4:]
			continue
		}
		if n > 1000 {
			t.Fatalf("band packet of %d bytes over side-band's 1000", n)
		}
		if data[4] == 1 {
			status.Write(data[5:n])
		}
		data = data[n:]
	}
	if c := strings.Count(status.String(), "ng refs/heads/branch-"); c != 50 {
		t.Fatalf("report-status rejects %d refs, want 50", c)
	}
}

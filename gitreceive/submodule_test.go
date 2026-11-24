package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestMain lets the hook tests run this test binary as /bin/gitreceived, so
// the real -check-submodule-url code decides each URL.
func TestMain(m *testing.M) {
	if os.Getenv("GITRECEIVE_TEST_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeLookup(m map[string][]string) func(string) ([]netip.Addr, error) {
	return func(host string) ([]netip.Addr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, ip := range ips {
			out = append(out, netip.MustParseAddr(ip))
		}
		return out, nil
	}
}

func TestCheckSubmoduleURL(t *testing.T) {
	lookup := fakeLookup(map[string][]string{
		"github.com":       {"140.82.112.3"},
		"git.example.com":  {"203.0.113.7", "2001:db8::7"},
		"rebind.example":   {"203.0.113.8", "10.0.0.5"},
		"overlay.example":  {"100.100.4.2"},
		"mapped.example":   {"::ffff:127.0.0.1"},
		"metadata.example": {"169.254.169.254"},
	})
	ok := []struct {
		raw, proto, url, pin string
		ssh                  bool
	}{
		{"https://github.com/flynn-examples/go-flynn-example.git", "https", "https://github.com/flynn-examples/go-flynn-example.git", "github.com:443:140.82.112.3", false},
		{"https://GitHub.com./x.git", "https", "https://GitHub.com./x.git", "github.com:443:140.82.112.3", false},
		{"https://git.example.com:8443/r.git", "https", "https://git.example.com:8443/r.git", "git.example.com:8443:203.0.113.7,[2001:db8::7]", false},
		{"https://203.0.113.9/r.git", "https", "https://203.0.113.9/r.git", "203.0.113.9:443:203.0.113.9", false},
		{"ssh://git@github.com/org/r.git", "ssh", "ssh://git@140.82.112.3/org/r.git", "github.com", true},
		{"ssh://git@github.com:2222/org/r.git", "ssh", "ssh://git@140.82.112.3:2222/org/r.git", "github.com", true},
		{"git@github.com:org/r.git", "ssh", "git@140.82.112.3:org/r.git", "github.com", true},
		{"git@git.example.com:org/r.git", "ssh", "git@203.0.113.7:org/r.git", "git.example.com", true},
	}
	for _, c := range ok {
		got, err := checkSubmoduleURL(c.raw, c.ssh, lookup)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.raw, err)
			continue
		}
		if got.Protocol != c.proto || got.URL != c.url || got.Pin != c.pin {
			t.Errorf("%s: got %+v, want %s %s %s", c.raw, got, c.proto, c.url, c.pin)
		}
	}

	bad := []struct {
		raw string
		ssh bool
	}{
		{"", true},
		{"file:///var/lib/repo", true},
		{"/tmp/repo-abc", true},
		{"../other", true},
		{"./sibling", true},
		{"repo-dir", true},
		{"-uhelp", true},
		{"--upload-pack=touch /tmp/x", true},
		{"http://blobstore.discoverd/x", true},
		{"https://blobstore.discoverd/x", true},
		{"https://controller.discoverd./x", true},
		{"https://controller/x", true},
		{"https://localhost/x", true},
		{"https://127.0.0.1/x", true},
		{"https://10.1.2.3/x", true},
		{"https://[::1]/x", true},
		{"https://[fd00::1]/x", true},
		{"https://100.100.0.5/x", true},
		{"https://rebind.example/x", true},
		{"https://overlay.example/x", true},
		{"https://mapped.example/x", true},
		{"https://metadata.example/x", true},
		{"https://unknown.example/x", true},
		{"git://github.com/x", true},
		{"ftp://github.com/x", true},
		{"ext::sh -c touch% /tmp/x", true},
		{"fd::17", true},
		{"https://github.com/x\nssh", true},
		{"https://github.com/a b", true},
		{"https://-github.com/x", true},
		{"https://git_hub.com/x", true},
		{"ssh://git@github.com/org/r.git", false},
		{"git@github.com:org/r.git", false},
		{"ssh://-oProxyCommand=x@github.com/r", true},
		{"ssh://git@-github.com/r", true},
		{"-oProxyCommand=x@github.com:r", true},
		{"git@10.0.0.1:r.git", true},
		{"git@controller.discoverd:r.git", true},
		{"git@[::1]:r.git", true},
		{"ssh://git@127.0.0.1/r.git", true},
	}
	for _, c := range bad {
		if got, err := checkSubmoduleURL(c.raw, c.ssh, lookup); err == nil {
			t.Errorf("%q (ssh=%v): accepted as %+v", c.raw, c.ssh, got)
		}
	}
}

func TestSubmoduleSSHAllowed(t *testing.T) {
	for _, c := range []struct {
		apps   string
		idents []string
		want   bool
	}{
		{"", []string{"id1", "web"}, false},
		{"*", []string{"id1"}, true},
		{"api, web", []string{"id1", "web"}, true},
		{"api", []string{"id1", "web"}, false},
		{"id1", []string{"id1", ""}, true},
		{"api", []string{"", ""}, false},
	} {
		if got := submoduleSSHAllowed(c.apps, c.idents); got != c.want {
			t.Errorf("%q %v: got %v", c.apps, c.idents, got)
		}
	}
}

// submoduleParent makes a repository whose only submodule points at url and
// whose gitlink records subRev. It returns the repository and its HEAD.
func submoduleParent(t *testing.T, url, subRev string) (string, string) {
	t.Helper()
	parent := t.TempDir()
	runGit(t, parent, "init", "-q", "-b", "main")
	runGit(t, parent, "config", "user.email", "t@example.com")
	runGit(t, parent, "config", "user.name", "t")
	modules := "[submodule \"dep\"]\n\tpath = dep\n\turl = " + url + "\n"
	if err := os.WriteFile(filepath.Join(parent, ".gitmodules"), []byte(modules), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "keep.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", ".gitmodules", "keep.txt")
	runGit(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+subRev+",dep")
	runGit(t, parent, "commit", "-q", "-m", "parent")
	return parent, strings.TrimSpace(string(mustGit(t, parent, "rev-parse", "HEAD")))
}

// submoduleOrigin makes a repository with secret.txt in it.
func submoduleOrigin(t *testing.T) (string, string) {
	t.Helper()
	sub := t.TempDir()
	runGit(t, sub, "init", "-q", "-b", "main")
	runGit(t, sub, "config", "user.email", "t@example.com")
	runGit(t, sub, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(sub, "secret.txt"), []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, sub, "add", "secret.txt")
	runGit(t, sub, "commit", "-q", "-m", "sub")
	return sub, strings.TrimSpace(string(mustGit(t, sub, "rev-parse", "HEAD")))
}

// runArchiveHook runs git-archive-all the way the pre-receive hook does, with
// this test binary standing in for /bin/gitreceived.
func runArchiveHook(t *testing.T, parent, rev string, extraEnv []string, apps ...string) (string, []byte, error) {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), "out.tar")
	args := append([]string{"bash", rev, tarPath}, apps...)
	hook := exec.Command("bash", append([]string{"-c", archiveTreeFunc + `
rev="$1"; out="$2"; shift 2
git-archive-all "$rev" "$@" > "$out"
`}, args...)...)
	hook.Dir = filepath.Join(parent, ".git")
	hook.Env = append(os.Environ(),
		"GIT_DIR="+filepath.Join(parent, ".git"),
		"GIT_QUARANTINE_PATH="+t.TempDir(),
		"SUBMODULE_URL_CHECK="+os.Args[0],
		"GITRECEIVE_TEST_RUN_MAIN=1",
	)
	hook.Env = append(hook.Env, extraEnv...)
	out, err := hook.CombinedOutput()
	return tarPath, out, err
}

// fakeSSH puts an ssh on PATH that records that it ran (and its arguments)
// and then fails.
func fakeSSH(t *testing.T) (pathEnv, marker string) {
	t.Helper()
	bin := t.TempDir()
	marker = filepath.Join(t.TempDir(), "ssh-called")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + bin + ":" + os.Getenv("PATH"), marker
}

// EDGE-02: the submodule URL comes from the pushed .gitmodules. The hook runs
// as root in the system gitreceive job, so it must not fetch local paths,
// internal services, option-looking URLs, or ssh for apps that did not opt in
// to the cluster key, and it must refuse before contacting the target.
func TestSubmoduleCloneRefusesUnsafeURL(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := submoduleOrigin(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer tls.Close()

	pathEnv, sshMarker := fakeSSH(t)
	touched := filepath.Join(t.TempDir(), "touched")

	for _, url := range []string{
		"file://" + sub,
		sub,
		"http://blobstore.discoverd/x",
		srv.URL + "/x.git",
		tls.URL + "/x.git",
		"--upload-pack=touch " + touched,
		"-uhelp",
		"ext::sh -c touch% " + touched,
		"ssh://git@github.com/org/private.git",
		"git@github.com:org/private.git",
	} {
		parent, rev := submoduleParent(t, url, subRev)
		tarPath, out, err := runArchiveHook(t, parent, rev, []string{pathEnv, "SSH_CLIENT_KEY_APPS=other-app"}, "app-id", "app-name")
		if err == nil {
			t.Errorf("%q: hook succeeded\n%s", url, out)
		}
		if listed, _ := exec.Command("tar", "-tf", tarPath).Output(); strings.Contains(string(listed), "secret.txt") {
			t.Errorf("%q: archive contains the submodule file", url)
		}
		if !strings.Contains(string(out), "submodule dep") {
			t.Errorf("%q: missing refusal message:\n%s", url, out)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("internal HTTP target was contacted %d times", n)
	}
	if _, err := os.Stat(sshMarker); err == nil {
		t.Error("ssh ran for an app that did not opt in")
	}
	if _, err := os.Stat(touched); err == nil {
		t.Error("an option-looking URL ran a command")
	}
}

// The hook still includes a submodule when the checker allows it, fetches
// only the recorded commit (depth 1), and runs under a time limit.
func TestSubmoduleFetchIsShallowAndPinned(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, _ := submoduleOrigin(t)
	if err := os.WriteFile(filepath.Join(sub, "later.txt"), []byte("later\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, sub, "add", "later.txt")
	runGit(t, sub, "commit", "-q", "-m", "later")
	second := strings.TrimSpace(string(mustGit(t, sub, "rev-parse", "HEAD")))
	parent, rev := submoduleParent(t, "file://"+sub, second)

	// A checker stand-in that allows file:// so the fetch can run offline.
	stub := filepath.Join(t.TempDir(), "check")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n[ \"$1\" = -check-submodule-url ] || exit 2\nprintf 'file\\n%s\\n\\n' \"$2\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{"SUBMODULE_URL_CHECK=" + stub}, "app-id")
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	listed, err := exec.Command("tar", "-tf", tarPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"dep/secret.txt", "dep/later.txt", "keep.txt"} {
		if !strings.Contains(string(listed), f) {
			t.Errorf("archive missing %s: %s", f, listed)
		}
	}
	if strings.Contains(archiveTreeFunc, "git clone") {
		t.Error("hook still uses a full git clone")
	}
	for _, want := range []string{"--depth 1", "timeout ", "ulimit -f", `-- "$fetch_url"`, "GIT_ALLOW_PROTOCOL"} {
		if !strings.Contains(archiveTreeFunc, want) {
			t.Errorf("hook missing %q", want)
		}
	}
}

func TestHookPassesAppIdentityToSubmoduleCheck(t *testing.T) {
	if !strings.Contains(string(prereceiveHook), `git-archive-all $newrev "$RECEIVE_APP" "$RECEIVE_APP_NAME"`) {
		t.Error("push hook does not pass the app ID and name")
	}
	if !strings.Contains(string(previewPrereceiveHook), `git-archive-all $newrev "$app"`) {
		t.Error("preview hook does not pass the preview app")
	}
	cmd, _ := gitCommand(gitEnv{App: "id1", AppName: "web"}, "true")
	found := false
	for _, e := range cmd.Env {
		if e == "RECEIVE_APP_NAME=web" {
			found = true
		}
	}
	if !found {
		t.Error("hook env lacks RECEIVE_APP_NAME")
	}
}

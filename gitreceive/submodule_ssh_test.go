package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// EDGE-26: the submodule fetch runs in the gitreceive image, so that image
// must install the OpenSSH client itself rather than rely on the base image
// happening to ship it (git only Recommends openssh-client, and packages.sh
// installs with --no-install-recommends).
func TestGitreceiveImageInstallsSSHClient(t *testing.T) {
	data, err := os.ReadFile("img/packages.sh")
	if err != nil {
		t.Fatal(err)
	}
	install := regexp.MustCompile(`(?m)^apt-get[^\n]*\binstall\b[^\n]*$`)
	lines := install.FindAllString(string(data), -1)
	found := false
	for _, l := range lines {
		for _, f := range strings.Fields(l) {
			if f == "openssh-client" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("gitreceive/img/packages.sh does not install openssh-client:\n%s", data)
	}
}

// fakeSSHTools puts an ssh and an ssh-keygen on PATH. ssh records its
// arguments and the mode and contents of its -i file, then runs the remote
// command locally, so git's ssh transport works against a local repository.
// ssh-keygen -F answers from plain (unhashed) known_hosts entries.
func fakeSSHTools(t *testing.T) (pathEnv, marker string) {
	t.Helper()
	bin := t.TempDir()
	marker = filepath.Join(t.TempDir(), "ssh-called")
	ssh := `#!/bin/sh
printf '%s\n' "$@" > ` + marker + `
key=""; prev=""
for a in "$@"; do [ "$prev" = "-i" ] && key="$a"; prev="$a"; done
if [ -n "$key" ] && [ -f "$key" ]; then
	(stat -c %a "$key" 2>/dev/null || stat -f %Lp "$key") > ` + marker + `.mode
	cat "$key" > ` + marker + `.key
	printf '%s\n' "$key" > ` + marker + `.path
fi
for last; do :; done
exec sh -c "$last"
`
	keygen := `#!/bin/sh
host=""; file=""
while [ $# -gt 0 ]; do
	case "$1" in
	-F) host="$2"; shift 2 ;;
	-f) file="$2"; shift 2 ;;
	*) shift ;;
	esac
done
[ -n "$host" ] && [ -f "$file" ] && grep -q "^$host " "$file"
`
	for name, body := range map[string]string{"ssh": ssh, "ssh-keygen": keygen} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return "PATH=" + bin + ":" + os.Getenv("PATH"), marker
}

// sshHome makes a HOME whose ~/.ssh/known_hosts is what start.sh writes from
// SSH_CLIENT_HOSTS.
func sshHome(t *testing.T, knownHosts string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(knownHosts), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

const testClientKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nnot-a-real-key\n-----END OPENSSH PRIVATE KEY-----"

// An opted-in app's ssh submodule is fetched through ssh with the cluster key
// staged for that fetch only: a 0600 file in a private directory under the
// key directory (tmpfs /dev/shm in the gitreceive job), removed afterwards.
func TestSubmoduleSSHFetchStagesKeyOnlyForTheFetch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := submoduleOrigin(t)
	pathEnv, marker := fakeSSHTools(t)
	home := sshHome(t, "203.0.113.5 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST\n")
	keyDir := t.TempDir()
	parent, rev := submoduleParent(t, "ssh://git@203.0.113.5"+sub, subRev)
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{
		pathEnv, "HOME=" + home, "SSH_CLIENT_KEY_APPS=app-name",
		"SSH_CLIENT_KEY=" + testClientKey, "SUBMODULE_KEY_DIR=" + keyDir,
	}, "app-id", "app-name")
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	listed, err := exec.Command("tar", "-tf", tarPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(listed), "dep/secret.txt") {
		t.Errorf("archive missing the ssh submodule: %s", listed)
	}
	args, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ssh did not run: %v", err)
	}
	for _, want := range []string{"IdentitiesOnly=yes", "BatchMode=yes", "StrictHostKeyChecking=yes", "HostKeyAlias=203.0.113.5", "git@203.0.113.5"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("ssh args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "StrictHostKeyChecking=no") || strings.Contains(string(args), "accept-new") {
		t.Errorf("ssh host key checking weakened:\n%s", args)
	}
	key, err := os.ReadFile(marker + ".key")
	if err != nil {
		t.Fatalf("ssh had no readable -i key file: %v", err)
	}
	if strings.TrimSpace(string(key)) != testClientKey {
		t.Errorf("staged key = %q", key)
	}
	if mode, _ := os.ReadFile(marker + ".mode"); strings.TrimSpace(string(mode)) != "600" {
		t.Errorf("staged key mode = %q, want 600", mode)
	}
	keyPath, _ := os.ReadFile(marker + ".path")
	if !strings.HasPrefix(strings.TrimSpace(string(keyPath)), keyDir+"/") {
		t.Errorf("key staged at %q, not under SUBMODULE_KEY_DIR %s", keyPath, keyDir)
	}
	if left, _ := os.ReadDir(keyDir); len(left) != 0 {
		t.Errorf("key directory not cleaned up: %v", left)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "cluster_client_key")); err == nil {
		t.Error("key left in ~/.ssh")
	}
}

// linkTools makes a PATH directory holding only the named tools, so a test
// can run the hook without an ssh client.
func linkTools(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		p, err := exec.LookPath(n)
		if err != nil {
			t.Skipf("%s is not installed", n)
		}
		if err := os.Symlink(p, filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// When an opted-in app's ssh submodule cannot be fetched because the image
// has no ssh client, no key is configured, or SSH_CLIENT_HOSTS has no key for
// the host, the push fails with a message naming what is missing, and ssh is
// never run (no fallback to unverified host keys).
func TestSubmoduleSSHFailsClosedWithClearError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	_, subRev := submoduleOrigin(t)
	parent, rev := submoduleParent(t, "ssh://git@203.0.113.5/org/private.git", subRev)
	pathEnv, marker := fakeSSHTools(t)
	known := "203.0.113.5 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST\n"
	noSSH := "PATH=" + linkTools(t, "git", "tar", "mktemp", "rm", "mkdir", "dirname", "env", "timeout", "cat", "chmod", "sh", "bash")

	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"no ssh client", []string{noSSH, "HOME=" + sshHome(t, known), "SSH_CLIENT_KEY=" + testClientKey},
			"ssh submodule URL for app app-name requires an OpenSSH client (ssh, ssh-keygen), which is not available in this gitreceive image"},
		{"no key", []string{pathEnv, "HOME=" + sshHome(t, known), "SSH_CLIENT_KEY="},
			"ssh submodule URL for app app-name requires the cluster SSH client key (gitreceive SSH_CLIENT_KEY), which is not set"},
		{"unknown host", []string{pathEnv, "HOME=" + sshHome(t, "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST\n"), "SSH_CLIENT_KEY=" + testClientKey},
			"ssh submodule URL for app app-name requires a host key for 203.0.113.5 in gitreceive SSH_CLIENT_HOSTS, which is not available"},
	} {
		os.Remove(marker)
		env := append(c.env, "SSH_CLIENT_KEY_APPS=app-name", "SUBMODULE_KEY_DIR="+t.TempDir())
		_, out, err := runArchiveHook(t, parent, rev, env, "app-id", "app-name")
		if err == nil {
			t.Errorf("%s: hook succeeded\n%s", c.name, out)
		}
		if !strings.Contains(string(out), c.want) {
			t.Errorf("%s: missing %q in:\n%s", c.name, c.want, out)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("%s: ssh ran", c.name)
		}
	}
}

// The ssh command the hook builds works with the real OpenSSH client: strict
// host key checking against a hashed known_hosts entry (ssh-keyscan -H, as
// the docs say) under HostKeyAlias, on a non-default port, with the staged
// key. A local sshd stands in for the git host; the checker is stubbed
// because it (correctly) refuses loopback addresses.
func TestSubmoduleSSHWithRealOpenSSH(t *testing.T) {
	for _, n := range []string{"git", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not installed", n)
		}
	}
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("sshd is not installed")
	}
	sub, subRev := submoduleOrigin(t)
	dir := t.TempDir()
	keygen := func(path string) {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v\n%s", err, out)
		}
	}
	keygen(filepath.Join(dir, "host_key"))
	keygen(filepath.Join(dir, "client_key"))
	clientPub, _ := os.ReadFile(filepath.Join(dir, "client_key.pub"))
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), clientPub, 0600); err != nil {
		t.Fatal(err)
	}
	clientKey, _ := os.ReadFile(filepath.Join(dir, "client_key"))
	hostPub, _ := os.ReadFile(filepath.Join(dir, "host_key.pub"))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cfg := fmt.Sprintf("ListenAddress 127.0.0.1:%d\nHostKey %s\nAuthorizedKeysFile %s\nPidFile %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\n",
		port, filepath.Join(dir, "host_key"), filepath.Join(dir, "authorized_keys"), filepath.Join(dir, "sshd.pid"))
	if err := os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshd, "-D", "-e", "-f", filepath.Join(dir, "sshd_config"))
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sshd: %v", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	logs := make(chan string, 100)
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			select {
			case logs <- s.Text():
			default:
			}
		}
	}()
	up := false
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			up = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !up {
		t.Skip("sshd did not start listening")
	}

	// known_hosts as the docs produce it (hashed), for the name the push
	// used, not the address the checker pinned.
	alias := "git.example.com"
	hostFields := strings.Fields(string(hostPub))
	home := sshHome(t, alias+" "+hostFields[0]+" "+hostFields[1]+"\n")
	if out, err := exec.Command("ssh-keygen", "-H", "-f", filepath.Join(home, ".ssh", "known_hosts")).CombinedOutput(); err != nil {
		t.Fatalf("hash known_hosts: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); strings.Contains(string(data), alias) {
		t.Fatalf("known_hosts not hashed: %s", data)
	}

	user := os.Getenv("USER")
	if user == "" {
		out, _ := exec.Command("id", "-un").Output()
		user = strings.TrimSpace(string(out))
	}
	fetchURL := fmt.Sprintf("ssh://%s@127.0.0.1:%d%s", user, port, sub)
	stub := filepath.Join(t.TempDir(), "check")
	if err := os.WriteFile(stub, []byte(fmt.Sprintf("#!/bin/sh\n[ \"$1\" = -check-submodule-url ] || exit 2\nprintf 'ssh\\n%%s\\n%%s\\n' '%s' '%s'\n", fetchURL, alias)), 0755); err != nil {
		t.Fatal(err)
	}
	keyDir := t.TempDir()
	parent, rev := submoduleParent(t, "ssh://git@"+alias+"/org/private.git", subRev)
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{
		"HOME=" + home, "SUBMODULE_URL_CHECK=" + stub, "SSH_CLIENT_KEY=" + string(clientKey),
		"SSH_CLIENT_KEY_APPS=app-name", "SUBMODULE_KEY_DIR=" + keyDir,
	}, "app-id", "app-name")
	if err != nil {
		close(logs)
		var sl []string
		for s := range logs {
			sl = append(sl, s)
		}
		t.Fatalf("archive over real ssh: %v\n%s\nsshd:\n%s", err, out, strings.Join(sl, "\n"))
	}
	listed, _ := exec.Command("tar", "-tf", tarPath).Output()
	if !strings.Contains(string(listed), "dep/secret.txt") {
		t.Errorf("archive missing the ssh submodule: %s", listed)
	}
	if left, _ := os.ReadDir(keyDir); len(left) != 0 {
		t.Errorf("key directory not cleaned up: %v", left)
	}

	// The same fetch with a host key that does not match is refused.
	other := filepath.Join(dir, "other_key")
	keygen(other)
	otherPub, _ := os.ReadFile(other + ".pub")
	f := strings.Fields(string(otherPub))
	home2 := sshHome(t, alias+" "+f[0]+" "+f[1]+"\n")
	_, out, err = runArchiveHook(t, parent, rev, []string{
		"HOME=" + home2, "SUBMODULE_URL_CHECK=" + stub, "SSH_CLIENT_KEY=" + string(clientKey),
		"SSH_CLIENT_KEY_APPS=app-name", "SUBMODULE_KEY_DIR=" + keyDir,
	}, "app-id", "app-name")
	if err == nil {
		t.Errorf("fetch with a mismatched host key succeeded\n%s", out)
	}
}

// start.sh writes SSH_CLIENT_HOSTS to known_hosts but never writes the
// cluster key to disk (the hook stages it per fetch), and drops the key from
// the environment when no app opted in.
func TestStartScriptNeverWritesKey(t *testing.T) {
	for _, apps := range []string{"", "web"} {
		home := t.TempDir()
		envOut := filepath.Join(t.TempDir(), "env")
		bin := t.TempDir()
		// start.sh execs /bin/gitreceived; record its env through a PATH
		// stand-in by making the script exec what GITRECEIVED points at.
		stand := filepath.Join(bin, "gitreceived")
		if err := os.WriteFile(stand, []byte("#!/bin/sh\nenv > "+envOut+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "start.sh")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GITRECEIVED=" + stand, "SSH_CLIENT_KEY=" + testClientKey, "SSH_CLIENT_KEY_APPS=" + apps, "SSH_CLIENT_HOSTS=github.com ssh-ed25519 AAAA"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("apps=%q: start.sh: %v\n%s", apps, err, out)
		}
		filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				if data, _ := os.ReadFile(p); strings.Contains(string(data), "not-a-real-key") {
					t.Errorf("apps=%q: key written to %s", apps, p)
				}
			}
			return nil
		})
		if data, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); err != nil || !strings.Contains(string(data), "github.com ssh-ed25519") {
			t.Errorf("apps=%q: known_hosts = %q, %v", apps, data, err)
		}
		env, _ := os.ReadFile(envOut)
		if hasKey := strings.Contains(string(env), "SSH_CLIENT_KEY=-----BEGIN"); hasKey != (apps != "") {
			t.Errorf("apps=%q: SSH_CLIENT_KEY in gitreceived env = %v", apps, hasKey)
		}
	}
}

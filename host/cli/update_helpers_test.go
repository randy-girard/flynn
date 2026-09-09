package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
)

func TestLinuxHostBinaryFiles(t *testing.T) {
	files := linuxHostBinaryFiles()
	if len(files) != 3 {
		t.Fatalf("expected 3 binaries, got %d", len(files))
	}
	arch := runtime.GOARCH
	want := []struct{ name, dest string }{
		{"flynn-host-linux-" + arch + ".gz", "flynn-host"},
		{"flynn-init-linux-" + arch + ".gz", "flynn-init"},
		{"flynn-linux-" + arch + ".gz", "flynn-linux-" + arch},
	}
	for i, f := range files {
		if f.name != want[i].name || f.destName != want[i].dest {
			t.Fatalf("[%d] got {%q,%q} want {%q,%q}", i, f.name, f.destName, want[i].name, want[i].dest)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SHA512SUMS")
	content := strings.Join([]string{
		"abc123  flynn-host-linux-amd64.gz",
		"def456 *flynn-init-linux-amd64.gz",
		"ghi789 ./flynn-linux-amd64.gz",
		"ignored-only-one-field",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := parseChecksums(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"flynn-host-linux-amd64.gz": "abc123",
		"flynn-init-linux-amd64.gz": "def456",
		"flynn-linux-amd64.gz":      "ghi789",
	}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got[%q]=%q want %q", k, got[k], v)
		}
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.gz")
	payload := []byte("checksum-payload")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(payload)
	expected := hex.EncodeToString(sum[:])
	if err := verifyChecksum(path, expected); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(path, "deadbeef"); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestHostIDs(t *testing.T) {
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.2:1113", nil, nil)
	got := hostIDs([]*cluster.Host{h1, h2})
	if len(got) != 2 || got[0] != "h1" || got[1] != "h2" {
		t.Fatalf("got %v", got)
	}
}

func TestIsPrivateIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.0.1", false},
		{"192.168.1.1", true},
		{"100.64.0.1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if got := isPrivateIP(ip); got != tc.want {
			t.Fatalf("isPrivateIP(%s)=%v want %v", tc.ip, got, tc.want)
		}
	}
}

func TestExtractTarball(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "flynn-vTEST.0.tar.gz")
	if err := writeTestUpdateTarball(tarball, "flynn-vTEST.0", map[string]string{
		"flynn-vTEST.0/bin/flynn-host": "host-bin",
		"flynn-vTEST.0/version":        "vTEST.0",
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	version, contentDir, err := extractTarball(tarball, dest)
	if err != nil {
		t.Fatal(err)
	}
	if version != "vTEST.0" {
		t.Fatalf("version=%q", version)
	}
	if contentDir != filepath.Join(dest, "flynn-vTEST.0") {
		t.Fatalf("contentDir=%q", contentDir)
	}
	got, err := os.ReadFile(filepath.Join(contentDir, "version"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "vTEST.0" {
		t.Fatalf("version file=%q", got)
	}
}

func TestExtractTarballRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "bad.tar.gz")
	if err := writeTestUpdateTarball(tarball, "flynn-vBAD", map[string]string{
		"../evil": "nope",
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := extractTarball(tarball, dest); err == nil {
		t.Fatal("expected path traversal error")
	}
}

func writeTestUpdateTarball(path, _ string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, body); err != nil {
			return err
		}
	}
	return nil
}

func TestApplyUpdateTimingFlags(t *testing.T) {
	origHealth, origDelay, origWait := updateHealthTimeout, updateInterHostDelay, updateWaitJobsTimeout
	defer func() {
		updateHealthTimeout, updateInterHostDelay, updateWaitJobsTimeout = origHealth, origDelay, origWait
	}()

	log := log15.New()
	args := &docopt.Args{String: map[string]string{
		"--health-timeout":    "12m",
		"--inter-host-delay":  "45s",
		"--wait-jobs-timeout": "3m",
	}}
	if err := applyUpdateTimingFlags(args, log); err != nil {
		t.Fatal(err)
	}
	if updateHealthTimeout != 12*time.Minute {
		t.Fatalf("health=%s", updateHealthTimeout)
	}
	if updateInterHostDelay != 45*time.Second {
		t.Fatalf("delay=%s", updateInterHostDelay)
	}
	if updateWaitJobsTimeout != 3*time.Minute {
		t.Fatalf("wait=%s", updateWaitJobsTimeout)
	}

	if err := applyUpdateTimingFlags(&docopt.Args{String: map[string]string{"--health-timeout": "nope"}}, log); err == nil {
		t.Fatal("expected invalid duration error")
	}
	if err := applyUpdateTimingFlags(&docopt.Args{String: map[string]string{"--inter-host-delay": "0s"}}, log); err == nil {
		t.Fatal("expected non-positive duration error")
	}
}

func TestSettleLocalRestartBeforeRemotesNoRestart(t *testing.T) {
	// --no-restart must skip settle entirely (no discoverd dependency).
	if err := settleLocalRestartBeforeRemotes(true, log15.New()); err != nil {
		t.Fatal(err)
	}
}

package fixer

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
)

func TestJobIPOnSubnet(t *testing.T) {
	if !jobIPOnSubnet("100.100.83.5", "100.100.83.1/24") {
		t.Fatal("expected 83.5 on 83.x subnet")
	}
	if jobIPOnSubnet("100.100.38.5", "100.100.83.1/24") {
		t.Fatal("expected stale 38.x address to be rejected")
	}
}

func TestFixMinHosts(t *testing.T) {
	n, err := FixMinHosts("3", 1)
	if err != nil || n != 3 {
		t.Fatalf("flag 3: %d %v", n, err)
	}
	n, err = FixMinHosts("", 5)
	if err != nil || n != 5 {
		t.Fatalf("detected: %d %v", n, err)
	}
	n, err = FixMinHosts("", 0)
	if err != nil || n != 1 {
		t.Fatalf("default 1: %d %v", n, err)
	}
	if _, err := FixMinHosts("0", 1); err == nil {
		t.Fatal("expected invalid 0")
	}
}

func TestResolveFixOptionsNonInteractive(t *testing.T) {
	f := &ClusterFixer{
		Interactive: func() bool { return false },
		Stdout:      ioDiscard(),
	}
	args := &docopt.Args{
		String: map[string]string{"--min-hosts": "2", "--peer-ips": "10.0.0.1", "-n": ""},
		Bool:   map[string]bool{"--yes": true},
	}
	min, peer, err := f.resolveFixOptions(args, 9)
	if err != nil || min != 2 || peer != "10.0.0.1" {
		t.Fatalf("got min=%d peer=%q err=%v", min, peer, err)
	}
}

func TestResolveFixOptionsInteractive(t *testing.T) {
	var out bytes.Buffer
	f := &ClusterFixer{
		Interactive: func() bool { return true },
		Stdin:       strings.NewReader("3\n10.0.0.5\nyes\n"),
		Stdout:      &out,
	}
	args := &docopt.Args{
		String: map[string]string{"--min-hosts": "", "--peer-ips": "", "-n": ""},
		Bool:   map[string]bool{"--yes": false},
	}
	min, peer, err := f.resolveFixOptions(args, 1)
	if err != nil || min != 3 || peer != "10.0.0.5" {
		t.Fatalf("got min=%d peer=%q err=%v out=%s", min, peer, err, out.String())
	}
	if !strings.Contains(out.String(), "Run cluster fix") {
		t.Fatalf("missing confirm prompt: %s", out.String())
	}
}

func TestResolveFixOptionsYesSkipsPrompts(t *testing.T) {
	var out bytes.Buffer
	f := &ClusterFixer{
		Interactive: func() bool { return true },
		Stdin:       strings.NewReader("this would hang if we prompted\n"),
		Stdout:      &out,
	}
	args := &docopt.Args{
		String: map[string]string{"--min-hosts": "4", "--peer-ips": "10.1.1.1", "-n": ""},
		Bool:   map[string]bool{"--yes": true},
	}
	min, peer, err := f.resolveFixOptions(args, 1)
	if err != nil || min != 4 || peer != "10.1.1.1" {
		t.Fatalf("got min=%d peer=%q err=%v", min, peer, err)
	}
	if strings.Contains(out.String(), "yes/no") {
		t.Fatalf("--yes must not prompt, got %s", out.String())
	}
}

func TestResolveFixOptionsAborted(t *testing.T) {
	f := &ClusterFixer{
		Interactive: func() bool { return true },
		Stdin:       strings.NewReader("\n\nno\n"),
		Stdout:      ioDiscard(),
	}
	args := &docopt.Args{
		String: map[string]string{"--min-hosts": "1", "--peer-ips": "", "-n": ""},
		Bool:   map[string]bool{"--yes": false},
	}
	if _, _, err := f.resolveFixOptions(args, 1); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want aborted, got %v", err)
	}
}

func TestFixControllerLoadsKeyFromJobs(t *testing.T) {
	src, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "controllerkey.FromHosts(") {
		t.Fatal("FixController must load AUTH_KEY from running controller jobs after SEC-028")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func ioDiscard() discard { return discard{} }

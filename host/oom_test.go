package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParseOOMKill(t *testing.T) {
	n, ok := parseOOMKill([]byte("low 0\nhigh 0\nmax 0\noom 1\noom_kill 3\n"))
	if !ok || n != 3 {
		t.Fatalf("got %d ok=%v", n, ok)
	}
	if _, ok := parseOOMKill([]byte("low 0\n")); ok {
		t.Fatal("expected missing oom_kill")
	}
}

func TestJobCgroupCandidates(t *testing.T) {
	got := jobCgroupCandidates("user", "host0-abc")
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0] != "/sys/fs/cgroup/flynn/user/host0-abc" {
		t.Fatalf("unified path %q", got[0])
	}
	empty := jobCgroupCandidates("", "id")
	if empty[0] != "/sys/fs/cgroup/flynn/user/id" {
		t.Fatalf("default partition %q", empty[0])
	}
}

func TestFindExistingDir(t *testing.T) {
	dir := t.TempDir()
	if got := findExistingDir([]string{"/no/such", dir}); got != dir {
		t.Fatalf("got %q", got)
	}
	if got := findExistingDir([]string{"/no/such"}); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestWatchMemoryEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.events")
	write := func(n int) {
		t.Helper()
		body := "low 0\noom 0\noom_kill " + strconv.Itoa(n) + "\n"
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(0)
	stop := make(chan struct{})
	ch, err := watchMemoryEvents(path, 20*time.Millisecond, stop)
	if err != nil {
		t.Fatal(err)
	}
	write(1)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for oom_kill increase")
	}
	close(stop)
	select {
	case _, ok := <-ch:
		if ok {
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Fatal("watcher did not stop")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}

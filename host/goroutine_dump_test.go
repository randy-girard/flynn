package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
)

func TestSIGUSR2WritesGoroutineDump(t *testing.T) {
	dir := t.TempDir()
	stop := handleGoroutineDumps(dir, log15.New())
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	var files []string
	for start := time.Now(); time.Since(start) < 5*time.Second; time.Sleep(20 * time.Millisecond) {
		files, _ = filepath.Glob(filepath.Join(dir, "flynn-host-goroutines-*.txt"))
		if len(files) == 1 {
			break
		}
	}
	if len(files) != 1 {
		t.Fatalf("want one goroutine dump in %s after SIGUSR2, got %v", dir, files)
	}
	var data []byte
	for start := time.Now(); time.Since(start) < 5*time.Second; time.Sleep(20 * time.Millisecond) {
		data, _ = os.ReadFile(files[0])
		if strings.Contains(string(data), "TestSIGUSR2WritesGoroutineDump") {
			break
		}
	}
	if !strings.Contains(string(data), "goroutine ") || !strings.Contains(string(data), "TestSIGUSR2WritesGoroutineDump") {
		t.Fatalf("dump does not hold full goroutine stacks:\n%s", data)
	}
	if fi, err := os.Stat(files[0]); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("dump mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
}

package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A shell started with the listener dup clears O_NONBLOCK. That flag is
// shared with the parent's socket, so Accept blocks in the kernel and
// Listener.Close waits forever. The duplicated fd must still accept after
// the parent closes, because the child serves on it.
func TestCloseListenerAfterShellClearsNonblock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("O_NONBLOCK sharing is observed through /proc on linux")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp := ln.(*net.TCPListener)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- http.Serve(tcp, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", tcp.Addr().String(), 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}

	f, err := tcp.File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cmd := exec.Command("/bin/bash", "-c", "sleep 30")
	cmd.ExtraFiles = []*os.File{f}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	// Bash clears O_NONBLOCK while Accept is parked in the poller. One
	// connection wakes that wait. The following Accept then blocks in the
	// kernel, which is the state where Listener.Close waits forever.
	wake, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	wake.Close()
	deadline = time.Now().Add(2 * time.Second)
	for !acceptBlocked() {
		if time.Now().After(deadline) {
			t.Fatal("accept did not block after the shell child started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- closeListener(tcp) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closeListener blocked after a shell child cleared O_NONBLOCK")
	}

	select {
	case err := <-serveErr:
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("http server did not return after the listener closed")
	}

	child, err := net.FileListener(f)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	payload := "ok"
	go func() {
		c, err := net.Dial("tcp", child.Addr().String())
		if err != nil {
			return
		}
		io.WriteString(c, payload)
		c.Close()
	}()
	child.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	c, err := child.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("child listener got %q", buf)
	}
}

func acceptBlocked() bool {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return false
	}
	for _, task := range tasks {
		wchan, err := os.ReadFile("/proc/self/task/" + task.Name() + "/wchan")
		if err != nil {
			continue
		}
		if strings.Contains(string(wchan), "accept") {
			return true
		}
	}
	return false
}

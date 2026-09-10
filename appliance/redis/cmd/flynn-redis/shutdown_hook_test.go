package main_test

import (
	"os"
	"strings"
	"testing"
)

func TestMainClosesRedisOnSIGTERM(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "shutdown.BeforeExit(func() { m.Close() })") {
		t.Fatal("flynn-redis must Close (SHUTDOWN SAVE redis-server) on SIGTERM, not only the heartbeater")
	}
	if strings.Contains(body, "shutdown.BeforeExit(func() { hb.Close() })") &&
		!strings.Contains(body, "shutdown.BeforeExit(func() { m.Close() })") {
		t.Fatal("heartbeater-only BeforeExit leaves redis-server to be SIGKILL'd with the cgroup")
	}
}

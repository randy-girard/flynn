package redis_test

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	redigo "github.com/garyburd/redigo/redis"
)

func skipWithoutRedisServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH")
	}
}

func redisDo(t *testing.T, p *Process, args ...interface{}) string {
	t.Helper()
	conn, err := redigo.Dial("tcp", "127.0.0.1:"+p.Port,
		redigo.DialPassword(p.Password),
		redigo.DialConnectTimeout(2*time.Second),
		redigo.DialReadTimeout(5*time.Second),
		redigo.DialWriteTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("dial redis: %v", err)
	}
	defer conn.Close()
	reply, err := redigo.String(conn.Do(args[0].(string), args[1:]...))
	if err == redigo.ErrNil {
		return ""
	}
	if err != nil {
		t.Fatalf("redis %v: %v", args, err)
	}
	return reply
}

func waitForAOF(t *testing.T, dataDir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := ioutil.ReadDir(dataDir)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if (strings.Contains(name, "appendonly") || strings.HasSuffix(name, ".aof")) && e.Size() > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	listing, _ := ioutil.ReadDir(dataDir)
	names := make([]string, 0, len(listing))
	for _, e := range listing {
		names = append(names, fmt.Sprintf("%s=%d", e.Name(), e.Size()))
	}
	t.Fatalf("appendonly AOF not written under %s: %s", dataDir, strings.Join(names, ", "))
}

// TestProcess_RestartKeepsSET is the flynn-host update path: Stop (SHUTDOWN SAVE)
// then Start on the same DataDir must return the key written before stop.
func TestProcess_RestartKeepsSET(t *testing.T) {
	skipWithoutRedisServer(t)
	p := NewProcess(t)
	defer os.RemoveAll(p.DataDir)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if got := redisDo(t, p, "SET", "smoke_probe", "pre-upgrade"); got != "OK" {
		t.Fatalf("SET = %q, want OK", got)
	}
	if err := p.Process.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Process.Stop()
	if got := redisDo(t, p, "GET", "smoke_probe"); got != "pre-upgrade" {
		t.Fatalf("GET after restart = %q, want pre-upgrade", got)
	}
}

// TestProcess_AOFWrittenWithoutShutdown covers SIGKILL of redis-server: AOF
// must land on disk shortly after SET so a replacement job can load it even
// if SHUTDOWN SAVE never runs.
func TestProcess_AOFWrittenWithoutShutdown(t *testing.T) {
	skipWithoutRedisServer(t)
	p := NewProcess(t)
	defer os.RemoveAll(p.DataDir)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Process.Stop()
	if got := redisDo(t, p, "SET", "smoke_probe", "pre-upgrade"); got != "OK" {
		t.Fatalf("SET = %q, want OK", got)
	}
	waitForAOF(t, p.DataDir)

	cfg, err := ioutil.ReadFile(filepath.Join(p.DataDir, "redis.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "appendonly yes") {
		t.Fatalf("generated redis.conf missing AOF:\n%s", cfg)
	}
}

package main

import (
	"os"
	"strings"
	"testing"
)

func TestFollowLogsUnlocksBeforeGetStreams(t *testing.T) {
	src, err := os.ReadFile("libcontainer_backend.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (c *Container) followLogs")
	if start < 0 {
		t.Fatal("followLogs not found")
	}
	rest := body[start:]
	next := strings.Index(rest, "\nfunc ")
	if next < 0 {
		t.Fatal("followLogs has no following func")
	}
	fn := rest[:next]
	if strings.Contains(fn, "defer c.l.logStreamMtx.Unlock()") {
		t.Fatal("followLogs must not defer-unlock across container I/O")
	}
	unlock := strings.Index(fn, "logStreamMtx.Unlock()")
	get := strings.Index(fn, "GetStreams()")
	if unlock < 0 || get < 0 || unlock > get {
		t.Fatal("followLogs must release logStreamMtx before GetStreams")
	}
}

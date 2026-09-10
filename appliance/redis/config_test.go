package redis

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestConfigTemplateEnablesAOF(t *testing.T) {
	var buf bytes.Buffer
	err := configTemplate.Execute(&buf, struct {
		ID, Port, DataDir, Password string
	}{"id", "6379", "/data", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"appendonly yes", "appendfsync everysec", "dbfilename dump.rdb"} {
		if !strings.Contains(got, want) {
			t.Errorf("redis.conf missing %q:\n%s", want, got)
		}
	}
}

func TestShutdownSaveAccepted(t *testing.T) {
	if !shutdownSaveAccepted(nil) {
		t.Fatal("nil must be accepted")
	}
	if !shutdownSaveAccepted(io.EOF) {
		t.Fatal("EOF must be accepted (Redis closes the connection on SHUTDOWN)")
	}
	if !shutdownSaveAccepted(errors.New("use of closed network connection")) {
		t.Fatal("closed connection must be accepted")
	}
	if shutdownSaveAccepted(errors.New("WRONGPASS invalid username-password pair")) {
		t.Fatal("auth errors must not look like a successful SHUTDOWN")
	}
	if !shutdownSaveAccepted(errors.New("read tcp 127.0.0.1:6379: connection reset by peer")) {
		t.Fatal("connection reset after SHUTDOWN is the daemon exiting")
	}
	if !shutdownSaveAccepted(errors.New("write: broken pipe")) {
		t.Fatal("broken pipe after SHUTDOWN is the daemon exiting")
	}
	if shutdownSaveAccepted(errors.New("NOSCRIPT No matching script")) {
		t.Fatal("Redis command errors must not look like a successful SHUTDOWN")
	}
	if shutdownSaveAccepted(errors.New("i/o timeout")) {
		t.Fatal("a hung SHUTDOWN must not be treated as success")
	}
}

func TestStopEscalatesToSIGKILLNotSEGV(t *testing.T) {
	src, err := os.ReadFile("process.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	stopAt := strings.Index(body, "func (p *Process) stop()")
	if stopAt < 0 {
		t.Fatal("stop() missing")
	}
	fn := body[stopAt:]
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "syscall.SIGKILL") {
		t.Fatal("stop() must escalate to SIGKILL after SHUTDOWN SAVE / SIGTERM")
	}
	if strings.Contains(fn, "syscall.SIGSEGV") {
		t.Fatal("stop() must not SIGSEGV redis-server (that skips SHUTDOWN SAVE)")
	}
	if !strings.Contains(fn, "stopWait := 10 * time.Second") {
		t.Fatal("stop() must finish well under flynn-host's 30s SIGKILL window")
	}
	if !strings.Contains(fn, "p.shutdownSave()") {
		t.Fatal("stop() must try SHUTDOWN SAVE before signalling")
	}
}

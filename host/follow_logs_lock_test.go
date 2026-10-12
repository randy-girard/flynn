package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStopContainer(t *testing.T) {
	errRPC := errors.New("unexpected EOF")
	errTimeout := errors.New("Timed out")
	for _, tc := range []struct {
		name    string
		sigErr  map[syscall.Signal]error
		waits   []error // results of successive waitStop calls
		want    error
		signals []syscall.Signal
	}{
		{name: "exits on SIGTERM", waits: []error{nil}, signals: []syscall.Signal{syscall.SIGTERM}},
		{name: "SIGTERM RPC fails", sigErr: map[syscall.Signal]error{syscall.SIGTERM: errRPC}, want: errRPC, signals: []syscall.Signal{syscall.SIGTERM}},
		{name: "SIGKILL after grace", waits: []error{errTimeout}, signals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}},
		{name: "SIGKILL reply lost but job ended", sigErr: map[syscall.Signal]error{syscall.SIGKILL: errRPC}, waits: []error{errTimeout, nil}, signals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}},
		{name: "SIGKILL failed and job still running", sigErr: map[syscall.Signal]error{syscall.SIGKILL: errRPC}, waits: []error{errTimeout, errTimeout}, want: errRPC, signals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}},
	} {
		var sent []syscall.Signal
		waits := tc.waits
		signal := func(sig int) error {
			sent = append(sent, syscall.Signal(sig))
			return tc.sigErr[syscall.Signal(sig)]
		}
		waitStop := func(time.Duration) error {
			if len(waits) == 0 {
				t.Fatalf("%s: unexpected waitStop", tc.name)
			}
			err := waits[0]
			waits = waits[1:]
			return err
		}
		if err := stopContainer(signal, waitStop, time.Second); err != tc.want {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if len(sent) != len(tc.signals) {
			t.Errorf("%s: signals = %v, want %v", tc.name, sent, tc.signals)
			continue
		}
		for i := range sent {
			if sent[i] != tc.signals[i] {
				t.Errorf("%s: signals = %v, want %v", tc.name, sent, tc.signals)
			}
		}
	}
}

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

func TestAttachTTYTeesIntoLogMuxWhenLoggingEnabled(t *testing.T) {
	src, err := os.ReadFile("libcontainer_backend.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "if req.Job.Job.Config.TTY {")
	if start < 0 {
		t.Fatal("TTY attach not found")
	}
	fn := body[start:]
	end := strings.Index(fn, "client.Stop()")
	if end > 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "DisableLog") || !strings.Contains(fn, "LineWriter") || !strings.Contains(fn, "io.MultiWriter") {
		t.Fatal("TTY attach must copy PTY output into logmux when disable_log is false")
	}
}

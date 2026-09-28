package cli

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestSetJobTTYSizeKeepsPagerEnv(t *testing.T) {
	req := &ct.NewJob{Env: map[string]string{"PAGER": "less", "LESS": "--quit-if-one-screen"}}
	setJobTTYSize(req, 120, 40, "xterm-256color")
	if !req.TTY {
		t.Fatal("TTY")
	}
	if req.Columns != 120 || req.Lines != 40 {
		t.Fatalf("size %d x %d", req.Columns, req.Lines)
	}
	if req.Env["TERM"] != "xterm-256color" || req.Env["COLUMNS"] != "120" || req.Env["LINES"] != "40" {
		t.Fatalf("env %+v", req.Env)
	}
	if req.Env["PAGER"] != "less" {
		t.Fatal("must keep PAGER so less can use q/space on the PTY")
	}
}

func TestSetJobTTYSizeNilEnv(t *testing.T) {
	req := &ct.NewJob{}
	setJobTTYSize(req, 80, 24, "xterm")
	if req.Env["TERM"] != "xterm" || !req.TTY {
		t.Fatalf("%+v", req)
	}
}

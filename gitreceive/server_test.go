package main

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/pkg/status"
)

func TestGitHandlerAuthAndRouting(t *testing.T) {
	auth := authorizer.New([]string{"cluster-secret"}, nil, nil, 0)
	h := newGitHandler(nil, auth)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", status.Path, nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/myapp.git/info/refs", nil))
	if rec.Code != 401 {
		t.Fatalf("unauthed=%d", rec.Code)
	}

	authed := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.SetBasicAuth("", "cluster-secret")
		h.ServeHTTP(rec, req)
		return rec
	}
	if got := authed("/not-a-git-service"); got.Code != 403 {
		t.Fatalf("unknown service=%d", got.Code)
	}
	if got := authed("/Bad_Name.git/info/refs"); got.Code != 403 {
		t.Fatalf("invalid app name=%d", got.Code)
	}
}

func TestGitPktLineAndSubCommand(t *testing.T) {
	if subCommand("git-receive-pack") != "receive-pack" || subCommand("git-upload-pack") != "upload-pack" {
		t.Fatal("subCommand")
	}
	var buf bytes.Buffer
	if err := pktLine(&buf, "ok"); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "0006ok" {
		t.Fatalf("pktLine=%q", got)
	}
	if err := pktFlush(&buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("0000")) {
		t.Fatalf("%q", buf.Bytes())
	}

	cmd, _ := gitCommand(gitEnv{App: "demo"}, "true")
	found := false
	for _, e := range cmd.Env {
		if e == "RECEIVE_APP=demo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("env=%v", cmd.Env)
	}
}

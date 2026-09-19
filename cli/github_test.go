package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
)

type fakeAppGitHub struct {
	conn *ct.GitHubRepoConnection
	dep  *ct.GitHubDeploy
	err  error
}

func (f *fakeAppGitHub) GetAppGitHub(string) (*ct.GitHubRepoConnection, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.conn == nil {
		return nil, ct.ErrNotFound
	}
	cp := *f.conn
	return &cp, nil
}

func (f *fakeAppGitHub) PutAppGitHub(_ string, conn *ct.GitHubRepoConnection) error {
	if f.err != nil {
		return f.err
	}
	cp := *conn
	if cp.Branch == "" {
		cp.Branch = "main"
	}
	f.conn = &cp
	return nil
}

func (f *fakeAppGitHub) DeleteAppGitHub(string) error { return f.err }

func (f *fakeAppGitHub) DeployAppGitHub(string, *ct.GitHubDeployRequest) (*ct.GitHubDeploy, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.dep == nil {
		return &ct.GitHubDeploy{Owner: "acme", Repo: "app", Branch: "main", SHA: "abc", JobID: "job-1"}, nil
	}
	return f.dep, nil
}

func TestConnectGitHub(t *testing.T) {
	usage := `
usage: flynn github:connect [--installation=<id>] [--branch=<branch>] [--auto-deploy] [--wait-checks] <owner/repo>
`
	args, err := docopt.Parse(usage, []string{"github:connect", "--auto-deploy", "--wait-checks", "acme/app"}, false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAppGitHub{}
	if err := connectGitHub(args, fake, "app-1"); err != nil {
		t.Fatal(err)
	}
	if fake.conn.Owner != "acme" || fake.conn.Repo != "app" || !fake.conn.AutoDeploy || !fake.conn.WaitForChecks {
		t.Fatalf("%+v", fake.conn)
	}
}

func TestPrintGitHubConn(t *testing.T) {
	var buf bytes.Buffer
	if err := printGitHubConn(&buf, &ct.GitHubRepoConnection{Owner: "acme", Repo: "app", AccountLogin: "acme", Branch: "main", AutoDeploy: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "acme/app") || !strings.Contains(buf.String(), "true") {
		t.Fatalf("%s", buf.String())
	}
}

func TestParseOnOff(t *testing.T) {
	if v, ok := parseOnOff("on"); !ok || !v {
		t.Fatal("on")
	}
	if v, ok := parseOnOff("off"); !ok || v {
		t.Fatal("off")
	}
	if _, ok := parseOnOff(""); ok {
		t.Fatal("empty")
	}
}

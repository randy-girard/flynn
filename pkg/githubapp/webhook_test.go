package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if err := VerifySignature("secret", body, header); err != nil {
		t.Fatal(err)
	}
	if err := VerifySignature("secret", body, "sha256=deadbeef"); err == nil {
		t.Fatal("bad hmac")
	}
	if err := VerifySignature("", body, header); err == nil {
		t.Fatal("empty secret")
	}
	if err := VerifySignature("secret", body, "sha1=abc"); err == nil {
		t.Fatal("wrong alg")
	}
}

func TestParsePushAndBranchFromRef(t *testing.T) {
	ev, err := ParsePushEvent([]byte(`{
		"ref":"refs/heads/main",
		"after":"abc123",
		"deleted":false,
		"repository":{"name":"app","full_name":"acme/app","owner":{"login":"acme"}},
		"installation":{"id":9}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if BranchFromRef(ev.Ref) != "main" || ev.After != "abc123" || ev.Installation.ID != 9 {
		t.Fatalf("%+v", ev)
	}
	if ev.Repository.OwnerLogin() != "acme" || ev.Repository.RepoName() != "app" {
		t.Fatalf("repo %+v", ev.Repository)
	}
	if BranchFromRef("refs/tags/v1") != "" {
		t.Fatal("tags are not deploy branches")
	}
}

func TestSplitOwnerRepoAndCloneURL(t *testing.T) {
	owner, repo, err := SplitOwnerRepo("acme/app")
	if err != nil || owner != "acme" || repo != "app" {
		t.Fatalf("%s %s %v", owner, repo, err)
	}
	owner, repo, err = SplitOwnerRepo("https://github.com/acme/app.git")
	if err != nil || owner != "acme" || repo != "app" {
		t.Fatalf("url %s %s %v", owner, repo, err)
	}
	if _, _, err := SplitOwnerRepo("nocolon"); err == nil {
		t.Fatal("expected error")
	}
	got := CloneURL("github.com", "acme", "app", "tok")
	if got != "https://x-access-token:tok@github.com/acme/app.git" {
		t.Fatalf("clone %s", got)
	}
	if CloneURL("", "acme", "app", "") != "https://github.com/acme/app.git" {
		t.Fatal("public clone")
	}
	if APIHostFromURL("") != "github.com" || APIHostFromURL("https://ghe.example/api/v3") != "ghe.example" {
		t.Fatal("api host")
	}
	if DefaultBranch("develop", "") != "develop" || DefaultBranch("", "feature") != "feature" || DefaultBranch("", "") != "main" {
		t.Fatal("default branch")
	}
	if !MatchesDeployBranch("main", "main") || MatchesDeployBranch("main", "dev") {
		t.Fatal("match branch")
	}
}

func TestParseCheckAndStatusEvents(t *testing.T) {
	cs, err := ParseCheckSuiteEvent([]byte(`{"action":"completed","check_suite":{"head_sha":"s","head_branch":"main","status":"completed","conclusion":"success"},"repository":{"full_name":"acme/app","name":"app","owner":{"login":"acme"}},"installation":{"id":1}}`))
	if err != nil || cs.CheckSuite.HeadSHA != "s" {
		t.Fatalf("%+v %v", cs, err)
	}
	cr, err := ParseCheckRunEvent([]byte(`{"action":"completed","check_run":{"head_sha":"s","status":"completed","conclusion":"success","check_suite":{"head_branch":"main"}},"repository":{"name":"app","owner":{"login":"acme"}},"installation":{"id":1}}`))
	if err != nil || cr.CheckRun.HeadSHA != "s" {
		t.Fatalf("%+v %v", cr, err)
	}
	st, err := ParseStatusEvent([]byte(`{"sha":"s","state":"success","branches":[{"name":"main"}],"repository":{"name":"app","owner":{"login":"acme"}},"installation":{"id":1}}`))
	if err != nil || st.SHA != "s" {
		t.Fatalf("%+v %v", st, err)
	}
}

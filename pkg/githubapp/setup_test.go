package githubapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupGuide(t *testing.T) {
	g := Setup("example.local", "", "")
	if len(g.Permissions) != 4 || len(g.Events) != 4 || len(g.Steps) != 7 {
		t.Fatalf("guide %+v", g)
	}
	ctrl, git := WebhookURLs("example.local")
	if ctrl != "https://controller.example.local/github/webhook" || git != "https://git.example.local/github/webhook" {
		t.Fatalf("%s %s", ctrl, git)
	}
	found := false
	for _, s := range g.Steps {
		if s.Title == "Set the webhook" && s.Body != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("webhook step")
	}
	if InstallURL("flynn-deploy") != "https://github.com/apps/flynn-deploy/installations/new" {
		t.Fatal("install url")
	}
	if InstallURL("") != "https://github.com/settings/apps" {
		t.Fatal("empty slug")
	}
}

func TestGitHubClientInstallationsReposAndChecks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt-token" {
			t.Errorf("jwt %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode([]Installation{{ID: 7, Account: Account{Login: "acme", Type: "Organization"}}})
	})
	mux.HandleFunc("/app/installations/7/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "ghs_install"})
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghs_install" {
			t.Errorf("install token %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"repositories": []Repo{{Name: "app", FullName: "acme/app", DefaultBranch: "main"}},
		})
	})
	mux.HandleFunc("/repos/acme/app/commits/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": "abc123"})
	})
	mux.HandleFunc("/repos/acme/app/commits/abc123/check-runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"check_runs": []map[string]string{{"status": "completed", "conclusion": "success"}},
		})
	})
	mux.HandleFunc("/repos/acme/app/commits/abc123/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"state":    "success",
			"statuses": []map[string]string{{"state": "success"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, 1, func() (string, error) { return "jwt-token", nil }, srv.Client())
	insts, err := c.ListInstallations()
	if err != nil || len(insts) != 1 || insts[0].Account.Login != "acme" {
		t.Fatalf("installations %+v %v", insts, err)
	}
	repos, err := c.ListRepos(7)
	if err != nil || len(repos) != 1 || repos[0].FullName != "acme/app" {
		t.Fatalf("repos %+v %v", repos, err)
	}
	inst, repo, err := c.FindRepo("acme", "app")
	if err != nil || inst.ID != 7 || repo.DefaultBranch != "main" {
		t.Fatalf("find %+v %+v %v", inst, repo, err)
	}
	sha, err := c.CommitSHA(7, "acme", "app", "main")
	if err != nil || sha != "abc123" {
		t.Fatalf("sha %s %v", sha, err)
	}
	state, err := c.ChecksForSHA(7, "acme", "app", "abc123")
	if err != nil || state != CheckPassed {
		t.Fatalf("checks %s %v", state, err)
	}
	if _, _, err := c.FindRepo("acme", "missing"); err == nil {
		t.Fatal("missing repo")
	}
}

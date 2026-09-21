package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/githubapp"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

type githubStore interface {
	GetConfig() (*ct.GitHubAppConfig, error)
	UpdateConfig(*ct.GitHubAppConfig) error
	GetConnection(appID string) (*ct.GitHubRepoConnection, error)
	ListByRepo(owner, repo string) ([]*ct.GitHubRepoConnection, error)
	PutConnection(*ct.GitHubRepoConnection) error
	UpdateDeployState(*ct.GitHubRepoConnection) error
	DeleteConnection(appID string) error
}

type githubAPI interface {
	ListInstallations() ([]githubapp.Installation, error)
	ListRepos(installationID int64) ([]githubapp.Repo, error)
	FindRepo(owner, repo string) (*githubapp.Installation, *githubapp.Repo, error)
	CommitSHA(installationID int64, owner, repo, ref string) (string, error)
	ChecksForSHA(installationID int64, owner, repo, sha string) (githubapp.CheckState, error)
	InstallationToken(installationID int64) (*githubapp.Token, error)
}

type taffyLauncher interface {
	Launch(app *ct.App, cloneURL, branch, sha string, meta map[string]string) (string, error)
}

func (c *controllerAPI) clusterDomain() string {
	if c.githubDomain != "" {
		return c.githubDomain
	}
	if d := os.Getenv("DEFAULT_ROUTE_DOMAIN"); d != "" {
		return d
	}
	return ""
}

func (c *controllerAPI) publicGitHubConfig(cfg *ct.GitHubAppConfig) *ct.GitHubAppConfig {
	out := *cfg
	out.HasPrivateKey = strings.TrimSpace(cfg.PrivateKey) != ""
	out.HasWebhookSecret = strings.TrimSpace(cfg.WebhookSecret) != ""
	out.Configured = cfg.AppID > 0 && out.HasPrivateKey && out.HasWebhookSecret
	out.PrivateKey = ""
	out.WebhookSecret = ""
	out.ClientSecret = ""
	out.InstallURL = githubapp.InstallURL(cfg.Slug)
	ctrl, git := githubapp.WebhookURLs(c.clusterDomain())
	out.WebhookURL = ctrl
	out.GitWebhookURL = git
	return &out
}

func (c *controllerAPI) GetGitHubApp(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	cfg, err := c.githubStore.GetConfig()
	if err != nil {
		respondWithError(w, err)
		return
	}
	out := c.publicGitHubConfig(cfg)
	httphelper.JSON(w, 200, map[string]interface{}{
		"config": out,
		"setup":  githubapp.Setup(c.clusterDomain(), out.WebhookURL, out.GitWebhookURL),
	})
}

func (c *controllerAPI) UpdateGitHubApp(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var in ct.GitHubAppConfig
	if err := httphelper.DecodeJSON(req, &in); err != nil {
		respondWithError(w, err)
		return
	}
	existing, err := c.githubStore.GetConfig()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if strings.TrimSpace(in.PrivateKey) == "" {
		in.PrivateKey = existing.PrivateKey
	}
	if strings.TrimSpace(in.WebhookSecret) == "" {
		in.WebhookSecret = existing.WebhookSecret
	}
	if strings.TrimSpace(in.ClientSecret) == "" {
		in.ClientSecret = existing.ClientSecret
	}
	if in.AppID < 0 {
		respondWithError(w, ct.ValidationError{Field: "app_id", Message: "must be >= 0"})
		return
	}
	if in.AppID > 0 {
		if _, err := githubapp.ParseRSAPrivateKey([]byte(in.PrivateKey)); err != nil {
			respondWithError(w, ct.ValidationError{Field: "private_key", Message: "must be a PEM-encoded RSA private key"})
			return
		}
		if strings.TrimSpace(in.WebhookSecret) == "" {
			respondWithError(w, ct.ValidationError{Field: "webhook_secret", Message: "is required"})
			return
		}
	}
	if in.APIURL == "" {
		in.APIURL = githubapp.DefaultAPI
	}
	if err := c.githubStore.UpdateConfig(&in); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, c.publicGitHubConfig(&in))
}

func (c *controllerAPI) ListGitHubInstallations(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	api, err := c.gitHubAPI()
	if err != nil {
		respondWithError(w, err)
		return
	}
	insts, err := api.ListInstallations()
	if err != nil {
		respondWithError(w, err)
		return
	}
	allow, restricted := c.githubInstallationAllowlist(ctx)
	out := make([]ct.GitHubInstallation, 0, len(insts))
	for _, in := range insts {
		if restricted {
			if _, ok := allow[in.ID]; !ok {
				continue
			}
		}
		out = append(out, ct.GitHubInstallation{ID: in.ID, Login: in.Account.Login, Type: in.Account.Type})
	}
	httphelper.JSON(w, 200, out)
}

func (c *controllerAPI) ListGitHubInstallationRepos(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	id, err := strconv.ParseInt(params.ByName("installation_id"), 10, 64)
	if err != nil || id <= 0 {
		respondWithError(w, ct.ValidationError{Field: "installation_id", Message: "is invalid"})
		return
	}
	allow, restricted := c.githubInstallationAllowlist(ctx)
	if restricted {
		if _, ok := allow[id]; !ok {
			httphelper.JSON(w, 200, []ct.GitHubRepo{})
			return
		}
	}
	api, err := c.gitHubAPI()
	if err != nil {
		respondWithError(w, err)
		return
	}
	repos, err := api.ListRepos(id)
	if err != nil {
		respondWithError(w, err)
		return
	}
	out := make([]ct.GitHubRepo, 0, len(repos))
	for _, r := range repos {
		out = append(out, ct.GitHubRepo{Name: r.Name, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private})
	}
	httphelper.JSON(w, 200, out)
}

// githubInstallationAllowlist returns GitHub App installation IDs already
// linked to apps the caller can github:write. restricted is false for cluster
// admins and for the first-connect case (github:write but no links yet), so
// the dashboard Connect GitHub picker still lists the GitHub App catalog.
func (c *controllerAPI) githubInstallationAllowlist(ctx context.Context) (allow map[int64]struct{}, restricted bool) {
	appIDs, restricted := authz.GitHubWriteAppIDs(authz.TokenFromContext(ctx))
	if !restricted {
		return nil, false
	}
	if c.githubStore == nil {
		return map[int64]struct{}{}, true
	}
	allow = make(map[int64]struct{})
	for _, appID := range appIDs {
		conn, err := c.githubStore.GetConnection(appID)
		if err != nil {
			continue
		}
		if conn != nil && conn.InstallationID > 0 {
			allow[conn.InstallationID] = struct{}{}
		}
	}
	if len(allow) == 0 {
		return nil, false
	}
	return allow, true
}

func (c *controllerAPI) GetAppGitHub(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	conn, err := c.githubStore.GetConnection(app.ID)
	if err != nil {
		if err == ct.ErrNotFound {
			w.WriteHeader(404)
			return
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, conn)
}

func (c *controllerAPI) PutAppGitHub(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	var in ct.GitHubRepoConnection
	if err := httphelper.DecodeJSON(req, &in); err != nil {
		respondWithError(w, err)
		return
	}
	in.AppID = app.ID
	if strings.Contains(in.Repo, "/") && in.Owner == "" {
		owner, repo, err := githubapp.SplitOwnerRepo(in.Repo)
		if err != nil {
			respondWithError(w, ct.ValidationError{Field: "repo", Message: err.Error()})
			return
		}
		in.Owner, in.Repo = owner, repo
	}
	if in.Owner == "" || in.Repo == "" {
		respondWithError(w, ct.ValidationError{Field: "repo", Message: "owner and repo are required"})
		return
	}
	api, err := c.gitHubAPI()
	if err != nil {
		respondWithError(w, err)
		return
	}
	var ghRepo *githubapp.Repo
	if in.InstallationID <= 0 {
		inst, repo, err := api.FindRepo(in.Owner, in.Repo)
		if err != nil {
			respondWithError(w, err)
			return
		}
		in.InstallationID = inst.ID
		in.AccountLogin = inst.Account.Login
		ghRepo = repo
	} else {
		repos, err := api.ListRepos(in.InstallationID)
		if err != nil {
			respondWithError(w, err)
			return
		}
		want := strings.ToLower(in.Owner + "/" + in.Repo)
		for i := range repos {
			if strings.ToLower(repos[i].FullName) == want {
				ghRepo = &repos[i]
				break
			}
		}
		if ghRepo == nil {
			respondWithError(w, ct.ValidationError{Field: "repo", Message: "the GitHub App installation cannot access that repository"})
			return
		}
		if in.AccountLogin == "" {
			insts, err := api.ListInstallations()
			if err == nil {
				for _, inst := range insts {
					if inst.ID == in.InstallationID {
						in.AccountLogin = inst.Account.Login
						break
					}
				}
			}
		}
	}
	in.Branch = githubapp.DefaultBranch(ghRepo.DefaultBranch, in.Branch)
	in.FullName = in.Owner + "/" + in.Repo
	if err := c.githubStore.PutConnection(&in); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &in)
}

func (c *controllerAPI) DeleteAppGitHub(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	if err := c.githubStore.DeleteConnection(app.ID); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

func (c *controllerAPI) DeployAppGitHub(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	var in ct.GitHubDeployRequest
	if req.Body != nil {
		_ = httphelper.DecodeJSON(req, &in)
	}
	dep, err := c.deployGitHub(app, in.Branch, in.SHA, false)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, dep)
}

func (c *controllerAPI) GitHubWebhook(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	if err != nil {
		respondWithError(w, err)
		return
	}
	cfg, err := c.githubStore.GetConfig()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if err := githubapp.VerifySignature(cfg.WebhookSecret, body, req.Header.Get(githubapp.HeaderSignature)); err != nil {
		httphelper.Error(w, httphelper.JSONError{Code: httphelper.UnauthorizedErrorCode, Message: err.Error()})
		return
	}
	event := req.Header.Get(githubapp.HeaderEvent)
	switch event {
	case githubapp.EventPing:
		httphelper.JSON(w, 200, map[string]string{"status": "ok"})
		return
	case githubapp.EventPush:
		if err := c.handleGitHubPush(body); err != nil {
			respondWithError(w, err)
			return
		}
	case githubapp.EventCheckSuite, githubapp.EventCheckRun, githubapp.EventStatus:
		if err := c.handleGitHubChecks(event, body); err != nil {
			respondWithError(w, err)
			return
		}
	}
	httphelper.JSON(w, 200, map[string]string{"status": "ok"})
}

func (c *controllerAPI) handleGitHubPush(body []byte) error {
	ev, err := githubapp.ParsePushEvent(body)
	if err != nil {
		return err
	}
	if ev.Deleted {
		return nil
	}
	branch := githubapp.BranchFromRef(ev.Ref)
	if branch == "" {
		return nil
	}
	owner, repo := ev.Repository.OwnerLogin(), ev.Repository.RepoName()
	conns, err := c.githubStore.ListByRepo(owner, repo)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		if !conn.AutoDeploy || !githubapp.MatchesDeployBranch(conn.Branch, branch) {
			continue
		}
		app, err := c.lookupApp(conn.AppID)
		if err != nil {
			return err
		}
		if conn.WaitForChecks {
			conn.PendingSHA = ev.After
			if err := c.githubStore.UpdateDeployState(conn); err != nil {
				return err
			}
			state, err := c.checksOrPending(conn, ev.After)
			if err != nil {
				return err
			}
			if state != githubapp.CheckPassed {
				continue
			}
		}
		if _, err := c.deployGitHub(app, branch, ev.After, true); err != nil {
			return err
		}
	}
	return nil
}

func (c *controllerAPI) handleGitHubChecks(event string, body []byte) error {
	var owner, repo, sha, branch string
	switch event {
	case githubapp.EventCheckSuite:
		ev, err := githubapp.ParseCheckSuiteEvent(body)
		if err != nil {
			return err
		}
		if ev.Action != "completed" && ev.CheckSuite.Status != "completed" {
			return nil
		}
		owner, repo = ev.Repository.OwnerLogin(), ev.Repository.RepoName()
		sha, branch = ev.CheckSuite.HeadSHA, ev.CheckSuite.HeadBranch
	case githubapp.EventCheckRun:
		ev, err := githubapp.ParseCheckRunEvent(body)
		if err != nil {
			return err
		}
		owner, repo = ev.Repository.OwnerLogin(), ev.Repository.RepoName()
		sha, branch = ev.CheckRun.HeadSHA, ev.CheckRun.CheckSuite.HeadBranch
	case githubapp.EventStatus:
		ev, err := githubapp.ParseStatusEvent(body)
		if err != nil {
			return err
		}
		owner, repo = ev.Repository.OwnerLogin(), ev.Repository.RepoName()
		sha = ev.SHA
		if len(ev.Branches) > 0 {
			branch = ev.Branches[0].Name
		}
	}
	if sha == "" {
		return nil
	}
	conns, err := c.githubStore.ListByRepo(owner, repo)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		if !conn.AutoDeploy || !conn.WaitForChecks {
			continue
		}
		if conn.PendingSHA != "" && conn.PendingSHA != sha {
			continue
		}
		if branch != "" && !githubapp.MatchesDeployBranch(conn.Branch, branch) {
			continue
		}
		state, err := c.checksOrPending(conn, sha)
		if err != nil {
			return err
		}
		if state == githubapp.CheckPending {
			if conn.PendingSHA == "" {
				conn.PendingSHA = sha
				_ = c.githubStore.UpdateDeployState(conn)
			}
			continue
		}
		if state != githubapp.CheckPassed {
			conn.PendingSHA = ""
			_ = c.githubStore.UpdateDeployState(conn)
			continue
		}
		app, err := c.lookupApp(conn.AppID)
		if err != nil {
			return err
		}
		if _, err := c.deployGitHub(app, conn.Branch, sha, true); err != nil {
			return err
		}
	}
	return nil
}

func (c *controllerAPI) checksOrPending(conn *ct.GitHubRepoConnection, sha string) (githubapp.CheckState, error) {
	api, err := c.gitHubAPI()
	if err != nil {
		return "", err
	}
	return api.ChecksForSHA(conn.InstallationID, conn.Owner, conn.Repo, sha)
}

func (c *controllerAPI) lookupApp(id string) (*ct.App, error) {
	if c.appRepo == nil {
		return &ct.App{ID: id, Name: id}, nil
	}
	data, err := c.appRepo.Get(id)
	if err != nil {
		return nil, err
	}
	return data.(*ct.App), nil
}

func (c *controllerAPI) gitHubAPI() (githubAPI, error) {
	if c.githubAPI != nil {
		return c.githubAPI, nil
	}
	cfg, err := c.githubStore.GetConfig()
	if err != nil {
		return nil, err
	}
	if cfg.AppID <= 0 || strings.TrimSpace(cfg.PrivateKey) == "" {
		return nil, ct.ValidationError{Field: "github", Message: "GitHub App is not configured. A cluster admin must run flynn-host github:configure or save credentials in Cluster → GitHub."}
	}
	key, err := githubapp.ParseRSAPrivateKey([]byte(cfg.PrivateKey))
	if err != nil {
		return nil, err
	}
	return githubapp.NewClient(cfg.APIURL, cfg.AppID, func() (string, error) {
		return githubapp.SignAppJWT(cfg.AppID, key, time.Now())
	}, c.githubHTTP), nil
}

func parseGitHubAppID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("app id is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid GitHub App id")
	}
	return id, nil
}

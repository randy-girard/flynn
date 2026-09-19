package main

import (
	"fmt"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/githubapp"
)

func (c *controllerAPI) deployGitHub(app *ct.App, branch, sha string, fromWebhook bool) (*ct.GitHubDeploy, error) {
	if app == nil {
		return nil, fmt.Errorf("missing app")
	}
	conn, err := c.githubStore.GetConnection(app.ID)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		branch = conn.Branch
	}
	if branch == "" {
		branch = "main"
	}
	api, err := c.gitHubAPI()
	if err != nil {
		return nil, err
	}
	if sha == "" {
		sha, err = api.CommitSHA(conn.InstallationID, conn.Owner, conn.Repo, branch)
		if err != nil {
			return nil, err
		}
	}
	tok, err := api.InstallationToken(conn.InstallationID)
	if err != nil {
		return nil, err
	}
	host := githubapp.APIHostFromURL("")
	if cfg, err := c.githubStore.GetConfig(); err == nil {
		host = githubapp.APIHostFromURL(cfg.APIURL)
	}
	cloneURL := githubapp.CloneURL(host, conn.Owner, conn.Repo, tok.Token)
	meta := map[string]string{
		"github":      "true",
		"github_user": conn.Owner,
		"github_repo": conn.Repo,
		"branch":      branch,
		"rev":         sha,
		"clone_url":   githubapp.CloneURL(host, conn.Owner, conn.Repo, ""),
		"app":         app.ID,
	}
	if fromWebhook {
		meta["github_webhook"] = "true"
	}
	launcher := c.taffy
	if launcher == nil {
		launcher = &liveTaffyLauncher{api: c}
	}
	jobID, err := launcher.Launch(app, cloneURL, branch, sha, meta)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	conn.PendingSHA = ""
	conn.LastDeploySHA = sha
	conn.LastDeployAt = &now
	_ = c.githubStore.UpdateDeployState(conn)
	return &ct.GitHubDeploy{
		AppID:    app.ID,
		JobID:    jobID,
		Owner:    conn.Owner,
		Repo:     conn.Repo,
		Branch:   branch,
		SHA:      sha,
		CloneURL: meta["clone_url"],
	}, nil
}

type liveTaffyLauncher struct {
	api *controllerAPI
}

func (l *liveTaffyLauncher) Launch(app *ct.App, cloneURL, branch, sha string, meta map[string]string) (string, error) {
	if l.api == nil || l.api.appRepo == nil || l.api.releaseRepo == nil {
		return "", fmt.Errorf("taffy launcher is not configured")
	}
	raw, err := l.api.appRepo.Get("taffy")
	if err != nil {
		return "", fmt.Errorf("get taffy app: %w", err)
	}
	taffyApp := raw.(*ct.App)
	if taffyApp.ReleaseID == "" {
		return "", fmt.Errorf("taffy has no release")
	}
	job, err := l.api.startDetachedJob(taffyApp, &ct.NewJob{
		ReleaseID:  taffyApp.ReleaseID,
		ReleaseEnv: true,
		Args: []string{
			"/bin/taffy",
			app.Name,
			cloneURL,
			branch,
			sha,
		},
		Meta: meta,
	})
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

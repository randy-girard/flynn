package data

import (
	"strings"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

type GitHubAppRepo struct {
	db *postgres.DB
}

func NewGitHubAppRepo(db *postgres.DB) *GitHubAppRepo {
	return &GitHubAppRepo{db: db}
}

func scanGitHubAppConfig(s postgres.Scanner) (*ct.GitHubAppConfig, error) {
	cfg := &ct.GitHubAppConfig{}
	var slug, privateKey, webhookSecret, clientID, clientSecret, apiURL *string
	err := s.Scan(&cfg.AppID, &slug, &privateKey, &webhookSecret, &clientID, &clientSecret, &apiURL, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if slug != nil {
		cfg.Slug = *slug
	}
	if privateKey != nil {
		cfg.PrivateKey = *privateKey
	}
	if webhookSecret != nil {
		cfg.WebhookSecret = *webhookSecret
	}
	if clientID != nil {
		cfg.ClientID = *clientID
	}
	if clientSecret != nil {
		cfg.ClientSecret = *clientSecret
	}
	if apiURL != nil {
		cfg.APIURL = *apiURL
	}
	cfg.HasPrivateKey = strings.TrimSpace(cfg.PrivateKey) != ""
	cfg.HasWebhookSecret = strings.TrimSpace(cfg.WebhookSecret) != ""
	cfg.Configured = cfg.AppID > 0 && cfg.HasPrivateKey && cfg.HasWebhookSecret
	return cfg, nil
}

func (r *GitHubAppRepo) GetConfig() (*ct.GitHubAppConfig, error) {
	return scanGitHubAppConfig(r.db.QueryRow("github_app_config_select"))
}

func (r *GitHubAppRepo) UpdateConfig(cfg *ct.GitHubAppConfig) error {
	return r.db.QueryRow("github_app_config_update",
		cfg.AppID,
		nullIfEmpty(cfg.Slug),
		nullIfEmpty(cfg.PrivateKey),
		nullIfEmpty(cfg.WebhookSecret),
		nullIfEmpty(cfg.ClientID),
		nullIfEmpty(cfg.ClientSecret),
		nullIfEmpty(cfg.APIURL),
	).Scan(&cfg.CreatedAt, &cfg.UpdatedAt)
}

func scanGitHubRepo(s postgres.Scanner) (*ct.GitHubRepoConnection, error) {
	c := &ct.GitHubRepoConnection{}
	var pending, lastSHA *string
	err := s.Scan(
		&c.ID, &c.AppID, &c.InstallationID, &c.AccountLogin, &c.Owner, &c.Repo, &c.Branch,
		&c.AutoDeploy, &c.WaitForChecks, &pending, &lastSHA, &c.LastDeployAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	if pending != nil {
		c.PendingSHA = *pending
	}
	if lastSHA != nil {
		c.LastDeploySHA = *lastSHA
	}
	c.FullName = c.Owner + "/" + c.Repo
	return c, nil
}

func (r *GitHubAppRepo) GetConnection(appID string) (*ct.GitHubRepoConnection, error) {
	return scanGitHubRepo(r.db.QueryRow("github_repo_select_by_app", appID))
}

func (r *GitHubAppRepo) ListByRepo(owner, repo string) ([]*ct.GitHubRepoConnection, error) {
	rows, err := r.db.Query("github_repo_list_by_repo", owner, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ct.GitHubRepoConnection
	for rows.Next() {
		c, err := scanGitHubRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *GitHubAppRepo) PutConnection(c *ct.GitHubRepoConnection) error {
	existing, err := r.GetConnection(c.AppID)
	if err == ErrNotFound {
		if c.ID == "" {
			c.ID = random.UUID()
		}
		return r.db.QueryRow("github_repo_insert",
			c.ID, c.AppID, c.InstallationID, c.AccountLogin, c.Owner, c.Repo, c.Branch, c.AutoDeploy, c.WaitForChecks,
		).Scan(&c.CreatedAt, &c.UpdatedAt)
	}
	if err != nil {
		return err
	}
	c.ID = existing.ID
	if c.PendingSHA == "" {
		c.PendingSHA = existing.PendingSHA
	}
	if c.LastDeploySHA == "" {
		c.LastDeploySHA = existing.LastDeploySHA
		c.LastDeployAt = existing.LastDeployAt
	}
	return r.db.QueryRow("github_repo_update",
		c.ID, c.InstallationID, c.AccountLogin, c.Owner, c.Repo, c.Branch, c.AutoDeploy, c.WaitForChecks,
		nullIfEmpty(c.PendingSHA), nullIfEmpty(c.LastDeploySHA), c.LastDeployAt,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
}

func (r *GitHubAppRepo) UpdateDeployState(c *ct.GitHubRepoConnection) error {
	return r.db.QueryRow("github_repo_update",
		c.ID, c.InstallationID, c.AccountLogin, c.Owner, c.Repo, c.Branch, c.AutoDeploy, c.WaitForChecks,
		nullIfEmpty(c.PendingSHA), nullIfEmpty(c.LastDeploySHA), c.LastDeployAt,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
}

func (r *GitHubAppRepo) DeleteConnection(appID string) error {
	return r.db.Exec("github_repo_delete", appID)
}

func nullIfEmpty(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

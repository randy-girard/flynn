package githubapp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const userAgent = "flynn-github-deploy"

// Installation is a GitHub App installation (an account that installed the app).
type Installation struct {
	ID      int64   `json:"id"`
	Account Account `json:"account"`
}

// Account is the GitHub user or organization that installed the app.
type Account struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// Repo is a repository the installation can access.
type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"clone_url"`
}

// Token is a short-lived installation access token.
type Token struct {
	Token     string
	ExpiresAt time.Time
}

// Client talks to the GitHub Apps API with a JWT or installation token.
type Client struct {
	HTTP  *http.Client
	API   string
	AppID int64
	jwt   func() (string, error)
}

// NewClient builds a GitHub Apps API client. jwtFn must return a signed App JWT.
func NewClient(api string, appID int64, jwtFn func() (string, error), httpClient *http.Client) *Client {
	if api == "" {
		api = DefaultAPI
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		HTTP:  httpClient,
		API:   strings.TrimRight(api, "/"),
		AppID: appID,
		jwt:   jwtFn,
	}
}

func (c *Client) do(method, path, token, accept string, body io.Reader) ([]byte, int, error) {
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = c.API + path
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return raw, res.StatusCode, nil
}

func (c *Client) appJWT() (string, error) {
	if c.jwt == nil {
		return "", fmt.Errorf("GitHub App JWT signer is not configured")
	}
	return c.jwt()
}

// ListInstallations lists accounts that installed the GitHub App.
func (c *Client) ListInstallations() ([]Installation, error) {
	token, err := c.appJWT()
	if err != nil {
		return nil, err
	}
	raw, code, err := c.do(http.MethodGet, "/app/installations", token, "", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("list GitHub App installations: HTTP %d", code)
	}
	var all []Installation
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("decode GitHub installations: %w", err)
	}
	return all, nil
}

// InstallationToken mints a repo-scoped token for an installation.
func (c *Client) InstallationToken(installationID int64) (*Token, error) {
	if installationID <= 0 {
		return nil, fmt.Errorf("GitHub installation id is required")
	}
	jwt, err := c.appJWT()
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	raw, code, err := c.do(http.MethodPost, path, jwt, "", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return nil, fmt.Errorf("GitHub installation token: HTTP %d", code)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode GitHub installation token: %w", err)
	}
	if out.Token == "" {
		return nil, fmt.Errorf("GitHub installation token missing")
	}
	return &Token{Token: out.Token, ExpiresAt: out.ExpiresAt}, nil
}

// ListRepos lists repositories an installation can access.
func (c *Client) ListRepos(installationID int64) ([]Repo, error) {
	tok, err := c.InstallationToken(installationID)
	if err != nil {
		return nil, err
	}
	var all []Repo
	path := "/installation/repositories?per_page=100"
	for path != "" {
		raw, code, err := c.do(http.MethodGet, path, tok.Token, "", nil)
		if err != nil {
			return nil, err
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("list GitHub installation repos: HTTP %d", code)
		}
		var page struct {
			Repositories []Repo `json:"repositories"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode GitHub repos: %w", err)
		}
		all = append(all, page.Repositories...)
		break
	}
	return all, nil
}

// FindRepo looks up owner/repo across installations and returns the matching installation.
func (c *Client) FindRepo(owner, repo string) (*Installation, *Repo, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	insts, err := c.ListInstallations()
	if err != nil {
		return nil, nil, err
	}
	want := strings.ToLower(owner + "/" + repo)
	for i := range insts {
		repos, err := c.ListRepos(insts[i].ID)
		if err != nil {
			return nil, nil, err
		}
		for j := range repos {
			if strings.ToLower(repos[j].FullName) == want {
				return &insts[i], &repos[j], nil
			}
		}
	}
	return nil, nil, fmt.Errorf("GitHub App cannot access %s/%s; install the app on that account and grant the repository", owner, repo)
}

// CommitSHA resolves a branch or tag to a commit SHA.
func (c *Client) CommitSHA(installationID int64, owner, repo, ref string) (string, error) {
	tok, err := c.InstallationToken(installationID)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	raw, code, err := c.do(http.MethodGet, path, tok.Token, "", nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("GitHub commit %s/%s@%s: HTTP %d", owner, repo, ref, code)
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode GitHub commit: %w", err)
	}
	if out.SHA == "" {
		return "", fmt.Errorf("GitHub commit %s/%s@%s missing sha", owner, repo, ref)
	}
	return out.SHA, nil
}

// ChecksForSHA returns GitHub Checks plus combined commit status for a SHA.
func (c *Client) ChecksForSHA(installationID int64, owner, repo, sha string) (CheckState, error) {
	tok, err := c.InstallationToken(installationID)
	if err != nil {
		return "", err
	}
	runs, err := c.listCheckRuns(tok.Token, owner, repo, sha)
	if err != nil {
		return "", err
	}
	combined, statuses, err := c.listStatuses(tok.Token, owner, repo, sha)
	if err != nil {
		return "", err
	}
	return EvaluateChecks(runs, statuses, combined, false), nil
}

func (c *Client) listCheckRuns(token, owner, repo, sha string) ([]CheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	raw, code, err := c.do(http.MethodGet, path, token, "application/vnd.github+json", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("GitHub check-runs %s/%s@%s: HTTP %d", owner, repo, sha, code)
	}
	var out struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode GitHub check-runs: %w", err)
	}
	runs := make([]CheckRun, 0, len(out.CheckRuns))
	for _, r := range out.CheckRuns {
		runs = append(runs, CheckRun{Status: r.Status, Conclusion: r.Conclusion})
	}
	return runs, nil
}

func (c *Client) listStatuses(token, owner, repo, sha string) (string, []CommitStatus, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/status", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	raw, code, err := c.do(http.MethodGet, path, token, "", nil)
	if err != nil {
		return "", nil, err
	}
	if code != http.StatusOK {
		return "", nil, fmt.Errorf("GitHub commit status %s/%s@%s: HTTP %d", owner, repo, sha, code)
	}
	var out struct {
		State    string `json:"state"`
		Statuses []struct {
			State string `json:"state"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, fmt.Errorf("decode GitHub commit status: %w", err)
	}
	st := make([]CommitStatus, 0, len(out.Statuses))
	for _, s := range out.Statuses {
		st = append(st, CommitStatus{State: s.State})
	}
	return out.State, st, nil
}

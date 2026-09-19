package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	HeaderEvent     = "X-GitHub-Event"
	HeaderDelivery  = "X-GitHub-Delivery"
	HeaderSignature = "X-Hub-Signature-256"

	EventPush       = "push"
	EventCheckSuite = "check_suite"
	EventCheckRun   = "check_run"
	EventStatus     = "status"
	EventPing       = "ping"
)

// VerifySignature checks GitHub's X-Hub-Signature-256 (sha256=<hex>).
func VerifySignature(secret string, body []byte, header string) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("github webhook secret is not configured")
	}
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "sha256=") {
		return fmt.Errorf("missing GitHub webhook signature")
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return fmt.Errorf("invalid GitHub webhook signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return fmt.Errorf("invalid GitHub webhook signature")
	}
	return nil
}

// PushEvent is the subset of a GitHub push webhook used for auto-deploys.
type PushEvent struct {
	Ref          string     `json:"ref"`
	After        string     `json:"after"`
	Deleted      bool       `json:"deleted"`
	Repository   Repository `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// CheckSuiteEvent is the subset of check_suite used to wait for CI.
type CheckSuiteEvent struct {
	Action     string `json:"action"`
	CheckSuite struct {
		HeadSHA    string `json:"head_sha"`
		HeadBranch string `json:"head_branch"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
	} `json:"check_suite"`
	Repository   Repository `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// CheckRunEvent is the subset of check_run used to wait for CI.
type CheckRunEvent struct {
	Action   string `json:"action"`
	CheckRun struct {
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
	} `json:"check_run"`
	Repository   Repository `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// StatusEvent is the subset of commit status used to wait for CI.
type StatusEvent struct {
	SHA      string `json:"sha"`
	State    string `json:"state"`
	Branches []struct {
		Name string `json:"name"`
	} `json:"branches"`
	Repository   Repository `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// Repository is the GitHub repo identity on webhook payloads.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"clone_url"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (r Repository) OwnerLogin() string {
	if r.Owner.Login != "" {
		return r.Owner.Login
	}
	owner, _, _ := SplitOwnerRepo(r.FullName)
	return owner
}

func (r Repository) RepoName() string {
	if r.Name != "" {
		return r.Name
	}
	_, repo, _ := SplitOwnerRepo(r.FullName)
	return repo
}

// ParsePushEvent decodes a GitHub push webhook body.
func ParsePushEvent(body []byte) (*PushEvent, error) {
	ev := &PushEvent{}
	if err := json.Unmarshal(body, ev); err != nil {
		return nil, fmt.Errorf("decode GitHub push event: %w", err)
	}
	return ev, nil
}

func ParseCheckSuiteEvent(body []byte) (*CheckSuiteEvent, error) {
	ev := &CheckSuiteEvent{}
	if err := json.Unmarshal(body, ev); err != nil {
		return nil, fmt.Errorf("decode GitHub check_suite event: %w", err)
	}
	return ev, nil
}

func ParseCheckRunEvent(body []byte) (*CheckRunEvent, error) {
	ev := &CheckRunEvent{}
	if err := json.Unmarshal(body, ev); err != nil {
		return nil, fmt.Errorf("decode GitHub check_run event: %w", err)
	}
	return ev, nil
}

func ParseStatusEvent(body []byte) (*StatusEvent, error) {
	ev := &StatusEvent{}
	if err := json.Unmarshal(body, ev); err != nil {
		return nil, fmt.Errorf("decode GitHub status event: %w", err)
	}
	return ev, nil
}

// BranchFromRef turns refs/heads/main into main.
func BranchFromRef(ref string) string {
	const prefix = "refs/heads/"
	if strings.HasPrefix(ref, prefix) {
		return strings.TrimPrefix(ref, prefix)
	}
	return ""
}

// SplitOwnerRepo parses "owner/repo".
func SplitOwnerRepo(full string) (owner, repo string, err error) {
	full = strings.TrimSpace(strings.TrimSuffix(full, ".git"))
	full = strings.TrimPrefix(full, "https://github.com/")
	full = strings.TrimPrefix(full, "http://github.com/")
	full = strings.TrimPrefix(full, "git@github.com:")
	parts := strings.Split(full, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected owner/repo, got %q", full)
	}
	return parts[0], parts[1], nil
}

// CloneURL builds an HTTPS clone URL that uses a GitHub App installation token.
func CloneURL(host, owner, repo, token string) string {
	if host == "" {
		host = "github.com"
	}
	if token == "" {
		return fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo)
	}
	return fmt.Sprintf("https://x-access-token:%s@%s/%s/%s.git", token, host, owner, repo)
}

// APIHostFromURL returns the git host for clone URLs from an API base.
func APIHostFromURL(apiURL string) string {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	switch {
	case apiURL == "" || apiURL == "https://api.github.com":
		return "github.com"
	case strings.HasSuffix(apiURL, "/api/v3"):
		return strings.TrimPrefix(strings.TrimSuffix(apiURL, "/api/v3"), "https://")
	default:
		return "github.com"
	}
}

// DefaultBranch returns the first non-empty candidate, preferring GitHub's
// default, then main, then master.
func DefaultBranch(repoDefault string, userEntered string) string {
	if b := strings.TrimSpace(userEntered); b != "" {
		return b
	}
	if b := strings.TrimSpace(repoDefault); b != "" {
		return b
	}
	return "main"
}

// MatchesDeployBranch is true when a webhook branch should trigger a deploy.
func MatchesDeployBranch(configured, incoming string) bool {
	return strings.EqualFold(strings.TrimSpace(configured), strings.TrimSpace(incoming))
}

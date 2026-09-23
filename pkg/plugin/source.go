package plugin

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// Resolved is the install source after aliases, local checkouts, and GitHub
// defaults are applied. Dir is set for an existing checkout. GitHub is set
// when the operator asked for a remote (or an alias fell back to one).
type Resolved struct {
	Input  string
	Dir    string
	Ref    string
	GitHub *GitHubSource
}

type GitHubSource struct {
	Host  string // github.com
	Owner string
	Repo  string
	URL   string // clone URL without credentials
	Ref   string
	API   string
}

func (r *Resolved) RefOr(def string) string {
	if r != nil && r.Ref != "" {
		return r.Ref
	}
	return def
}

func looksLikeGitURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	switch {
	case strings.HasPrefix(s, "git@"),
		strings.HasPrefix(s, "ssh://"),
		strings.HasPrefix(s, "git://"),
		strings.HasPrefix(s, "https://"),
		strings.HasPrefix(s, "http://"):
		return true
	case strings.HasPrefix(s, "github.com/"):
		return true
	default:
		return false
	}
}

// ParseGitHubURL extracts owner/repo from a git or GitHub URL.
func ParseGitHubURL(raw string) (*GitHubSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty git URL")
	}
	src := &GitHubSource{URL: raw, Host: "github.com"}
	switch {
	case strings.HasPrefix(raw, "git@"):
		// git@host:owner/repo.git
		rest := strings.TrimPrefix(raw, "git@")
		host, repoPath, ok := strings.Cut(rest, ":")
		if !ok {
			return nil, fmt.Errorf("invalid git SSH URL %q", raw)
		}
		src.Host = host
		if err := splitOwnerRepo(src, repoPath); err != nil {
			return nil, err
		}
	case strings.HasPrefix(raw, "github.com/"):
		if err := splitOwnerRepo(src, strings.TrimPrefix(raw, "github.com/")); err != nil {
			return nil, err
		}
		src.URL = "https://github.com/" + src.Owner + "/" + src.Repo + ".git"
	default:
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid git URL %q: %w", raw, err)
		}
		if u.Host != "" {
			src.Host = u.Host
		}
		if err := splitOwnerRepo(src, strings.TrimPrefix(u.Path, "/")); err != nil {
			return nil, err
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			src.URL = raw
		}
	}
	if src.Host == "github.com" {
		src.API = "https://api.github.com"
	} else {
		src.API = "https://" + src.Host + "/api/v3"
	}
	return src, nil
}

func splitOwnerRepo(src *GitHubSource, repoPath string) error {
	repoPath = strings.Trim(repoPath, "/")
	repoPath = strings.TrimSuffix(repoPath, ".git")
	parts := strings.Split(repoPath, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("git URL must be owner/repo, got %q", repoPath)
	}
	src.Owner = parts[0]
	src.Repo = parts[1]
	return nil
}

// Resolve turns a CLI source (path, alias, or git URL) into a local directory
// and/or a GitHub release to pull. Local checkouts win so Vagrant/dev keep
// working; missing siblings fall back to the official catalog, then
// github_org / plugins.json.
func Resolve(opts InstallOptions) (*Resolved, error) {
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		return nil, fmt.Errorf("plugin source is required")
	}
	cfg, err := LoadConfig(opts.PluginsFile)
	if err != nil {
		return nil, err
	}
	if opts.GitHubOrg != "" {
		cfg.GitHubOrg = opts.GitHubOrg
	}
	out := &Resolved{Input: source, Ref: strings.TrimSpace(opts.Ref)}

	if looksLikeGitURL(source) {
		gh, err := ParseGitHubURL(source)
		if err != nil {
			return nil, err
		}
		applyGitHub(out, gh, cfg, out.Ref)
		return out, nil
	}

	if dir, ok := existingDir(source, opts.Cwd); ok {
		out.Dir = dir
		return out, nil
	}

	alias, aliased := cfg.Aliases[source]
	if IsPrivatePluginName(source) && !privatePluginOverride(alias, aliased) {
		return nil, &PrivateCatalogError{Name: source}
	}
	if !aliased {
		if strings.ContainsAny(source, `/\`) || strings.HasPrefix(source, ".") {
			return nil, &NotFoundError{Source: source, Tried: source}
		}
		alias = Alias{}
	}

	if out.Ref == "" {
		out.Ref = alias.Ref
	}
	if alias.Path != "" {
		if dir, ok := existingDir(alias.Path, opts.Cwd); ok {
			out.Dir = dir
			return out, nil
		}
	}
	gitURL := alias.URL
	if gitURL == "" {
		gitURL = cfg.GitHubURL(source)
	}
	gh, err := ParseGitHubURL(gitURL)
	if err != nil {
		tried := alias.Path
		if tried == "" {
			tried = gitURL
		}
		return nil, &NotFoundError{Source: source, Tried: tried}
	}
	if alias.Repo != "" && !strings.Contains(alias.Repo, "/") {
		gh.Repo = alias.Repo
	}
	applyGitHub(out, gh, cfg, firstNonEmpty(out.Ref, alias.Ref))
	return out, nil
}

func privatePluginOverride(alias Alias, aliased bool) bool {
	if !aliased {
		return false
	}
	return alias.Path != "" || alias.URL != "" || alias.Repo != ""
}

func applyGitHub(out *Resolved, gh *GitHubSource, cfg *Config, ref string) {
	if cfg != nil && cfg.GitHubAPI != "" {
		gh.API = cfg.GitHubAPI
	}
	gh.Ref = ref
	out.GitHub = gh
	out.Ref = ref
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (g *GitHubSource) ReleaseAssetURL(name string) string {
	if g == nil {
		return ""
	}
	tag := g.Ref
	if tag == "" || tag == "latest" {
		return ""
	}
	return fmt.Sprintf("https://%s/%s/%s/releases/download/%s/%s", g.Host, g.Owner, g.Repo, tag, name)
}

func (g *GitHubSource) String() string {
	if g == nil {
		return ""
	}
	s := path.Join(g.Owner, g.Repo)
	if g.Ref != "" {
		return s + "@" + g.Ref
	}
	return s
}

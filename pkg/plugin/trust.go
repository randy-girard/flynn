package plugin

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

const clusterSecretsFlag = "--yes"

// IsOfficialPlugin reports whether m comes from Flynn's embedded catalog
// (name/alias) AND its source is exactly the catalog repo on github.com under
// the official org. A known name with a different host, owner/repo, or no
// repo identity at all (for example a bare local directory) is third-party.
func IsOfficialPlugin(m *Manifest, resolved *Resolved) bool {
	if m == nil {
		return false
	}
	cat := catalogPlugin(m.Name)
	if cat == nil {
		for _, a := range m.Aliases {
			if cat = catalogPlugin(a); cat != nil {
				break
			}
		}
	}
	if cat == nil {
		return false
	}
	host, owner, repo := githubIdentity(m, resolved)
	if !strings.EqualFold(host, "github.com") || owner == "" || repo == "" {
		return false
	}
	return samePluginRepo(owner+"/"+repo, cat.RepoSlug(officialGitHubOrg()))
}

func catalogPlugin(name string) *KnownPlugin {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, p := range KnownPlugins() {
		for _, n := range p.Names() {
			if n == name {
				out := p
				return &out
			}
		}
	}
	return nil
}

// githubIdentity returns the git host and owner/repo the plugin was sourced
// from. A resolved source wins; otherwise the manifest's github_repo (always
// github.com). Empty values mean no identity is known.
func githubIdentity(m *Manifest, resolved *Resolved) (host, owner, repo string) {
	if resolved != nil && resolved.GitHub != nil {
		g := resolved.GitHub
		return strings.TrimSpace(g.Host), strings.TrimSpace(g.Owner), strings.TrimSpace(g.Repo)
	}
	if m == nil {
		return "", "", ""
	}
	slug := strings.Trim(strings.TrimSuffix(strings.TrimSpace(m.GitHubRepo), ".git"), "/")
	if slug == "" {
		return "", "", ""
	}
	if !strings.Contains(slug, "/") {
		return "github.com", officialGitHubOrg(), slug
	}
	owner, repo, _ = strings.Cut(slug, "/")
	return "github.com", owner, repo
}

// samePluginRepo compares owner/repo exactly (case-insensitive, .git ignored).
func samePluginRepo(got, want string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".git")
		return strings.Trim(s, "/")
	}
	got, want = norm(got), norm(want)
	return got != "" && got == want
}

func clusterSecretsNotice(official bool, name string) string {
	name = strings.TrimSpace(name)
	if official {
		return fmt.Sprintf("plugin %s is in Flynn's official catalog. Its jobs receive CONTROLLER_KEY, DISCOVERD_AUTH_KEY, and access-token keys so appliance admin HTTP and discoverd can authenticate. That is cluster-admin equivalent.", name)
	}
	return fmt.Sprintf("plugin %s is not in Flynn's official catalog. Installing it injects CONTROLLER_KEY, DISCOVERD_AUTH_KEY, and access-token keys into the plugin job environment. That is cluster-admin equivalent; only install plugins you trust.", name)
}

func thirdPartySecretsError(name string) error {
	return fmt.Errorf("third-party plugin %s injects cluster secrets (CONTROLLER_KEY, DISCOVERD_AUTH_KEY, access-token keys). Re-run with %s to accept, or confirm on a TTY", strings.TrimSpace(name), clusterSecretsFlag)
}

// confirmClusterSecrets always logs the trust note. Official catalog plugins
// proceed. Third-party first installs require --yes or an interactive yes;
// updates of an already-installed third-party plugin log the note and continue.
func (in *Installer) confirmClusterSecrets(m *Manifest, resolved *Resolved, opts InstallOptions, alreadyInstalled bool) error {
	if m == nil {
		return nil
	}
	official := IsOfficialPlugin(m, resolved)
	in.logf("%s", clusterSecretsNotice(official, m.Name))
	if official {
		return nil
	}
	if alreadyInstalled {
		in.logf("plugin %s is already installed; cluster secrets stay on the new release (pass %s to acknowledge on the next first install)", m.Name, clusterSecretsFlag)
		return nil
	}
	if opts.Yes {
		in.logf("accepted cluster-secret injection via %s", clusterSecretsFlag)
		return nil
	}
	if !in.interactive() {
		return thirdPartySecretsError(m.Name)
	}
	fmt.Fprintf(in.writer(), "Inject cluster secrets into %s? [y/N]: ", m.Name)
	line, err := bufio.NewReader(in.reader()).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("confirm cluster secrets: %w", err)
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "y" || ans == "yes" {
		return nil
	}
	return fmt.Errorf("install of third-party plugin %s declined", m.Name)
}

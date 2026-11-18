package plugin

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestIsOfficialPluginByNameAndRepo(t *testing.T) {
	off := &Resolved{GitHub: &GitHubSource{Host: "github.com", Owner: "randy-girard", Repo: "flynn-plugin-redis"}}
	if !IsOfficialPlugin(&Manifest{Name: "redis"}, off) {
		t.Fatal("catalog name")
	}
	if !IsOfficialPlugin(&Manifest{Name: "mysql"}, &Resolved{GitHub: &GitHubSource{Host: "github.com", Owner: "randy-girard", Repo: "flynn-plugin-mysql"}}) {
		t.Fatal("mysql alias")
	}
	if IsOfficialPlugin(&Manifest{Name: "widget"}, nil) {
		t.Fatal("unknown name")
	}
	if IsOfficialPlugin(&Manifest{Name: "redis"}, &Resolved{
		GitHub: &GitHubSource{Host: "github.com", Owner: "evil", Repo: "flynn-plugin-redis"},
	}) {
		t.Fatal("same name, different owner is third-party")
	}
	if IsOfficialPlugin(&Manifest{Name: "redis", GitHubRepo: "evil/flynn-plugin-redis"}, nil) {
		t.Fatal("manifest github_repo override")
	}
	if !IsOfficialPlugin(&Manifest{Name: "otel", GitHubRepo: "randy-girard/flynn-plugin-otel"}, nil) {
		t.Fatal("manifest official repo")
	}
}

func TestConfirmClusterSecretsOfficialNoPrompt(t *testing.T) {
	var out bytes.Buffer
	in := &Installer{Stdout: &out, Interactive: func() bool { return true }}
	err := in.confirmClusterSecrets(&Manifest{Name: "redis"}, &Resolved{GitHub: &GitHubSource{Host: "github.com", Owner: "randy-girard", Repo: "flynn-plugin-redis"}}, InstallOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "official catalog") || !strings.Contains(out.String(), "CONTROLLER_KEY") {
		t.Fatalf("%q", out.String())
	}
}

func TestConfirmClusterSecretsThirdPartyRequiresYes(t *testing.T) {
	var out bytes.Buffer
	in := &Installer{Stdout: &out, Stdin: strings.NewReader(""), Interactive: func() bool { return false }}
	err := in.confirmClusterSecrets(&Manifest{Name: "widget"}, nil, InstallOptions{}, false)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v", err)
	}
	err = in.confirmClusterSecrets(&Manifest{Name: "widget"}, nil, InstallOptions{Yes: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "accepted cluster-secret injection") {
		t.Fatalf("%q", out.String())
	}
}

func TestConfirmClusterSecretsInteractive(t *testing.T) {
	in := &Installer{
		Stdout:      io.Discard,
		Stdin:       strings.NewReader("no\n"),
		Interactive: func() bool { return true },
	}
	err := in.confirmClusterSecrets(&Manifest{Name: "widget"}, nil, InstallOptions{}, false)
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("got %v", err)
	}
	in.Stdin = strings.NewReader("yes\n")
	if err := in.confirmClusterSecrets(&Manifest{Name: "widget"}, nil, InstallOptions{}, false); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmClusterSecretsSkipsPromptWhenAlreadyInstalled(t *testing.T) {
	in := &Installer{Stdout: io.Discard, Interactive: func() bool { return false }}
	if err := in.confirmClusterSecrets(&Manifest{Name: "widget"}, nil, InstallOptions{}, true); err != nil {
		t.Fatal(err)
	}
}

func TestIsOfficialPluginRequiresGitHubHost(t *testing.T) {
	m := &Manifest{Name: "redis"}
	src := func(host string) *Resolved {
		return &Resolved{GitHub: &GitHubSource{Host: host, Owner: "randy-girard", Repo: "flynn-plugin-redis"}}
	}
	if !IsOfficialPlugin(m, src("github.com")) {
		t.Fatal("github.com official repo must be official")
	}
	if !IsOfficialPlugin(m, src("GitHub.com")) {
		t.Fatal("host compare is case-insensitive")
	}
	for _, host := range []string{"ghe.attacker.example", "github.com.evil.example", "", "evilgithub.com"} {
		if IsOfficialPlugin(m, src(host)) {
			t.Fatalf("host %q must be third-party", host)
		}
	}
	// End to end through the URL parser: same owner/repo on another host.
	gh, err := ParseGitHubURL("https://ghe.attacker.example/randy-girard/flynn-plugin-redis")
	if err != nil {
		t.Fatal(err)
	}
	if IsOfficialPlugin(m, &Resolved{GitHub: gh}) {
		t.Fatal("ghe host with the official owner/repo must be third-party")
	}
	gh, err = ParseGitHubURL("git@ghe.attacker.example:randy-girard/flynn-plugin-redis.git")
	if err != nil {
		t.Fatal(err)
	}
	if IsOfficialPlugin(m, &Resolved{GitHub: gh}) {
		t.Fatal("ssh ghe host must be third-party")
	}
	gh, err = ParseGitHubURL("https://github.com/randy-girard/flynn-plugin-redis.git")
	if err != nil {
		t.Fatal(err)
	}
	if !IsOfficialPlugin(m, &Resolved{GitHub: gh}) {
		t.Fatal("real github.com URL must be official")
	}
}

func TestIsOfficialPluginNoIdentityIsThirdParty(t *testing.T) {
	if IsOfficialPlugin(&Manifest{Name: "redis"}, nil) {
		t.Fatal("catalog name with no repo identity must be third-party")
	}
	if IsOfficialPlugin(&Manifest{Name: "redis"}, &Resolved{Dir: "/tmp/anything"}) {
		t.Fatal("local directory with no identity must be third-party")
	}
	if IsOfficialPlugin(&Manifest{Name: "redis"}, &Resolved{GitHub: &GitHubSource{Host: "github.com"}}) {
		t.Fatal("empty owner/repo must be third-party")
	}
}

func TestIsOfficialPluginExactOwnerRepo(t *testing.T) {
	m := &Manifest{Name: "redis"}
	for _, gh := range []*GitHubSource{
		{Host: "github.com", Owner: "", Repo: "flynn-plugin-redis"},
		{Host: "github.com", Owner: "evil", Repo: "flynn-plugin-redis"},
		{Host: "github.com", Owner: "randy-girard", Repo: "flynn-plugin-redis-evil"},
		{Host: "github.com", Owner: "x/randy-girard", Repo: "flynn-plugin-redis"},
	} {
		if IsOfficialPlugin(m, &Resolved{GitHub: gh}) {
			t.Fatalf("%+v must be third-party", gh)
		}
	}
	if !IsOfficialPlugin(m, &Resolved{GitHub: &GitHubSource{Host: "github.com", Owner: "Randy-Girard", Repo: "flynn-plugin-redis.git"}}) {
		t.Fatal("case and .git suffix are normalised")
	}
}

// Behavioural: installing the official-looking repo from another host must hit
// the third-party gate (needs --yes), not pass silently.
func TestConfirmClusterSecretsForeignHostRequiresYes(t *testing.T) {
	gh, err := ParseGitHubURL("https://ghe.attacker.example/randy-girard/flynn-plugin-redis")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := &Installer{Stdout: &out, Stdin: strings.NewReader(""), Interactive: func() bool { return false }}
	err = in.confirmClusterSecrets(&Manifest{Name: "redis"}, &Resolved{GitHub: gh}, InstallOptions{}, false)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("foreign host install must require --yes, got %v", err)
	}
	if strings.Contains(out.String(), "is in Flynn's official catalog") {
		t.Fatalf("must not be described as official: %q", out.String())
	}
	// A local checkout with no repo identity is not official either.
	err = in.confirmClusterSecrets(&Manifest{Name: "redis"}, &Resolved{Dir: "/src/redis"}, InstallOptions{}, false)
	if err == nil {
		t.Fatal("identity-less install must require --yes")
	}
}

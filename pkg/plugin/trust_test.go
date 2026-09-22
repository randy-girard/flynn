package plugin

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestIsOfficialPluginByNameAndRepo(t *testing.T) {
	if !IsOfficialPlugin(&Manifest{Name: "redis"}, nil) {
		t.Fatal("catalog name")
	}
	if !IsOfficialPlugin(&Manifest{Name: "mysql"}, nil) {
		t.Fatal("mysql alias")
	}
	if IsOfficialPlugin(&Manifest{Name: "widget"}, nil) {
		t.Fatal("unknown name")
	}
	if !IsOfficialPlugin(&Manifest{Name: "redis"}, &Resolved{
		GitHub: &GitHubSource{Owner: "randy-girard", Repo: "flynn-plugin-redis"},
	}) {
		t.Fatal("official github")
	}
	if IsOfficialPlugin(&Manifest{Name: "redis"}, &Resolved{
		GitHub: &GitHubSource{Owner: "evil", Repo: "flynn-plugin-redis"},
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
	err := in.confirmClusterSecrets(&Manifest{Name: "redis"}, nil, InstallOptions{}, false)
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

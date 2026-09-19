package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeGitHubHost(t *testing.T) {
	cases := map[string]string{
		"":                DefaultGitHubHost,
		"github":          DefaultGitHubHost,
		"GitHub":          DefaultGitHubHost,
		"github.com":      DefaultGitHubHost,
		" git.example ":   "git.example",
		"ghe.example.com": "ghe.example.com",
	}
	for in, want := range cases {
		if got := NormalizeGitHubHost(in); got != want {
			t.Errorf("NormalizeGitHubHost(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCredentialStatusReportsAPINeverToken(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")
	path := filepath.Join(t.TempDir(), "plugin-credentials.json")
	set, api, err := CredentialStatus(path, "github")
	if err != nil || set || api != "" {
		t.Fatalf("empty: set=%v api=%q err=%v", set, api, err)
	}

	if err := SetGitHubCredentials(path, "github", "ghp_secret", "https://git.example.com/api/v3"); err != nil {
		t.Fatal(err)
	}
	set, api, err = CredentialStatus(path, "github.com")
	if err != nil || !set || api != "https://git.example.com/api/v3" {
		t.Fatalf("stored: set=%v api=%q err=%v", set, api, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ghp_secret") {
		t.Fatal("file must store the token")
	}
}

func TestUnsetGitHubCredentialsReportsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-credentials.json")
	removed, err := UnsetGitHubCredentials(path, "github")
	if err != nil || removed {
		t.Fatalf("nothing stored: removed=%v err=%v", removed, err)
	}
	if err := SetGitHubCredentials(path, "ghe.example.com", "ghp_ghe", "https://ghe.example.com/api/v3"); err != nil {
		t.Fatal(err)
	}
	removed, err = UnsetGitHubCredentials(path, "github")
	if err != nil || removed {
		t.Fatalf("other host: removed=%v err=%v", removed, err)
	}
	removed, err = UnsetGitHubCredentials(path, "ghe.example.com")
	if err != nil || !removed {
		t.Fatalf("ghe: removed=%v err=%v", removed, err)
	}
}

func TestReadTokenTrimsAndEmpty(t *testing.T) {
	got, err := ReadToken(strings.NewReader("  ghp_from_stdin \n"))
	if err != nil || got != "ghp_from_stdin" {
		t.Fatalf("ReadToken=%q err=%v", got, err)
	}
	got, err = ReadToken(strings.NewReader("  \n"))
	if err != nil || got != "" {
		t.Fatalf("empty ReadToken=%q err=%v", got, err)
	}
}

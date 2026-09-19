package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestReadCredentialTokenPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("  ghp_piped \n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	tok, err := readCredentialTokenFrom("", credentialIO{stdin: r, stderr: ioDiscard()})
	if err != nil || tok != "ghp_piped" {
		t.Fatalf("pipe: tok=%q err=%v", tok, err)
	}
}

func TestReadCredentialTokenEmptyPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	_, err = readCredentialTokenFrom("", credentialIO{stdin: r, stderr: ioDiscard()})
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "paste") {
		t.Fatalf("empty pipe: %v", err)
	}
}

func TestReadCredentialTokenMissingStdin(t *testing.T) {
	_, err := readCredentialTokenFrom("", credentialIO{stderr: ioDiscard()})
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "paste") {
		t.Fatalf("nil stdin: %v", err)
	}
}

func TestReadCredentialTokenTTYPaste(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })

	var stderr bytes.Buffer
	tok, err := readCredentialTokenFrom("", credentialIO{
		stdin:  devNull,
		stderr: &stderr,
		isTerminal: func(*os.File) bool {
			return true
		},
		readHidden: func(*os.File) (string, error) {
			return "  ghp_pasted  \n", nil
		},
	})
	if err != nil || tok != "ghp_pasted" {
		t.Fatalf("tty: tok=%q err=%v", tok, err)
	}
	if !strings.Contains(stderr.String(), "Paste a GitHub token") {
		t.Fatalf("prompt: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "ghp_pasted") {
		t.Fatal("must not echo token")
	}
}

func TestReadCredentialTokenTTYFallbackLine(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })

	var stderr bytes.Buffer
	_, err = readCredentialTokenFrom("", credentialIO{
		stdin:  devNull,
		stderr: &stderr,
		isTerminal: func(*os.File) bool {
			return true
		},
		readHidden: func(*os.File) (string, error) {
			return "", os.ErrInvalid
		},
	})
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "paste") {
		t.Fatalf("tty fallback on /dev/null: %v", err)
	}
	if !strings.Contains(stderr.String(), "could not hide input") {
		t.Fatalf("fallback hint: %q", stderr.String())
	}
}

func TestReadCredentialTokenNonTTYCharDevice(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })
	_, err = readCredentialTokenFrom("", credentialIO{
		stdin:  devNull,
		stderr: ioDiscard(),
		isTerminal: func(*os.File) bool {
			return false
		},
	})
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "paste") {
		t.Fatalf("non-tty char device: %v", err)
	}
}

func TestReadCredentialTokenTTYEmpty(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })
	_, err = readCredentialTokenFrom("", credentialIO{
		stdin:  devNull,
		stderr: ioDiscard(),
		isTerminal: func(*os.File) bool {
			return true
		},
		readHidden: func(*os.File) (string, error) {
			return "  \n", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "paste") {
		t.Fatalf("empty paste: %v", err)
	}
}

func TestFileIsTerminalPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { r.Close() })
	if fileIsTerminal(r) {
		t.Fatal("pipe is not a terminal")
	}
	if _, err := readHiddenFromTerminal(r); err == nil {
		t.Fatal("hidden read on a pipe must fail")
	}
}

func TestPluginCredentialsHostRequired(t *testing.T) {
	for _, name := range []string{"plugin:credentials-set", "plugin:credentials-unset", "plugin:credentials-show"} {
		cmd := commands[name]
		_, err := docopt.Parse(cmd.usage, []string{name}, false, "", false, false)
		if err == nil {
			t.Errorf("%s allowed missing host", name)
		}
	}
	args := parsePluginCmd(t, "plugin:credentials-set", "plugin:credentials-set", "github")
	host, err := githubCredentialsHost(args)
	if err != nil || host != "github.com" {
		t.Fatalf("github -> %q %v", host, err)
	}
	if _, err := githubCredentialsHost(&docopt.Args{String: map[string]string{"<host>": ""}}); err == nil || !strings.Contains(err.Error(), "host is required") {
		t.Fatalf("empty host: %v", err)
	}
}

func TestPluginCredentialsSetUnsetShow(t *testing.T) {
	t.Setenv(plugin.EnvGitHubToken, "")
	t.Setenv(plugin.EnvGitHubTokenAlt, "")
	creds := filepath.Join(t.TempDir(), "plugin-credentials.json")
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("ghp_secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	in := credentialIO{credsPath: creds, stdin: os.Stdin, stdout: &stdout, stderr: &stderr}

	args := parsePluginCmd(t, "plugin:credentials-set", "plugin:credentials-set", "github", "--token-file", tokenFile, "--api", "https://git.example.com/api/v3")
	if err := setPluginGitHubCredentials(args, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "github.com credentials set") {
		t.Fatalf("set stdout=%q", stdout.String())
	}
	if strings.Contains(stdout.String(), "ghp_secret") || strings.Contains(stderr.String(), "ghp_secret") {
		t.Fatalf("set echoed token: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	args = parsePluginCmd(t, "plugin:credentials-show", "plugin:credentials-show", "github")
	if err := showPluginGitHubCredentials(args, in); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "github.com credentials: set") || !strings.Contains(got, "api: https://git.example.com/api/v3") {
		t.Fatalf("show set: %q", got)
	}
	if strings.Contains(got, "ghp_secret") {
		t.Fatalf("show printed token: %q", got)
	}

	stdout.Reset()
	args = parsePluginCmd(t, "plugin:credentials-unset", "plugin:credentials-unset", "github.com")
	if err := unsetPluginGitHubCredentials(args, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "github.com credentials: removed") {
		t.Fatalf("unset: %q", stdout.String())
	}

	stdout.Reset()
	if err := unsetPluginGitHubCredentials(args, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "github.com credentials: nothing stored") {
		t.Fatalf("second unset: %q", stdout.String())
	}

	stdout.Reset()
	args = parsePluginCmd(t, "plugin:credentials-show", "plugin:credentials-show", "github")
	if err := showPluginGitHubCredentials(args, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "github.com credentials: unset") {
		t.Fatalf("show unset: %q", stdout.String())
	}
}

func TestPluginCredentialsSetFromPipe(t *testing.T) {
	t.Setenv(plugin.EnvGitHubToken, "")
	t.Setenv(plugin.EnvGitHubTokenAlt, "")
	creds := filepath.Join(t.TempDir(), "plugin-credentials.json")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("ghp_from_pipe\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	var stdout bytes.Buffer
	args := parsePluginCmd(t, "plugin:credentials-set", "plugin:credentials-set", "ghe.example.com", "--api", "https://ghe.example.com/api/v3")
	if err := setPluginGitHubCredentials(args, credentialIO{
		credsPath: creds,
		stdin:     r,
		stdout:    &stdout,
		stderr:    ioDiscard(),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "ghp_from_pipe") {
		t.Fatalf("piped set echoed token: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ghe.example.com credentials set") {
		t.Fatalf("ghe set: %q", stdout.String())
	}
	tok, api, err := plugin.TokenForHost("ghe.example.com", creds)
	if err != nil || tok != "ghp_from_pipe" || api != "https://ghe.example.com/api/v3" {
		t.Fatalf("stored: tok=%q api=%q err=%v", tok, api, err)
	}
}

func TestPluginCredentialsHelpMentionsPasteFileAndPipe(t *testing.T) {
	help := pluginCredentialsSetUsage
	for _, want := range []string{"paste", "--token-file", "cat /root/github.token |", "<host>"} {
		if !strings.Contains(help, want) {
			t.Fatalf("set usage missing %q:\n%s", want, help)
		}
	}
}

func ioDiscard() *bytes.Buffer {
	return &bytes.Buffer{}
}

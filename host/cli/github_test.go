package cli

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
)

type fakeGitHubController struct {
	cfg *ct.GitHubAppConfig
	err error
}

func (f *fakeGitHubController) GetGitHubApp() (*ct.GitHubAppConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.cfg == nil {
		return &ct.GitHubAppConfig{}, nil
	}
	return f.cfg, nil
}

func (f *fakeGitHubController) UpdateGitHubApp(cfg *ct.GitHubAppConfig) error {
	if f.err != nil {
		return f.err
	}
	cp := *cfg
	f.cfg = &cp
	return nil
}

func TestRunGitHubSetup(t *testing.T) {
	var buf bytes.Buffer
	if err := runGitHubSetup(&buf, "example.local"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Contents") || !strings.Contains(out, "Push") || !strings.Contains(out, "controller.example.local/github/webhook") {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(out, "Create a GitHub App") {
		t.Fatalf("steps missing: %s", out)
	}
}

func TestRunGitHubStatus(t *testing.T) {
	var buf bytes.Buffer
	fake := &fakeGitHubController{cfg: &ct.GitHubAppConfig{Configured: true, AppID: 9, HasPrivateKey: true, HasWebhookSecret: true, Slug: "flynn-deploy"}}
	if err := runGitHubStatus(fake, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "configured") || !strings.Contains(buf.String(), "9") {
		t.Fatalf("%s", buf.String())
	}
}

func TestRunGitHubConfigureValidatesKey(t *testing.T) {
	usage := `
usage: flynn-host github:configure --app-id=<id> --private-key-file=<path> --webhook-secret=<secret> [--slug=<slug>] [--client-id=<id>] [--client-secret=<secret>] [--api-url=<url>]
`
	args, err := docopt.Parse(usage, []string{"github:configure", "--app-id=1", "--private-key-file=/tmp/k.pem", "--webhook-secret=s"}, false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	err = runGitHubConfigure(args, &fakeGitHubController{}, func(string) ([]byte, error) { return []byte("not-a-key"), nil })
	if err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("%v", err)
	}
}

func TestRunGitHubConfigureSaves(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	usage := `
usage: flynn-host github:configure --app-id=<id> --private-key-file=<path> --webhook-secret=<secret> [--slug=<slug>] [--client-id=<id>] [--client-secret=<secret>] [--api-url=<url>]
`
	args, err := docopt.Parse(usage, []string{"github:configure", "--app-id=42", "--private-key-file=k.pem", "--webhook-secret=s", "--slug=flynn-deploy"}, false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitHubController{}
	if err := runGitHubConfigure(args, fake, func(string) ([]byte, error) { return pemBytes, nil }); err != nil {
		t.Fatal(err)
	}
	if fake.cfg == nil || fake.cfg.AppID != 42 || fake.cfg.Slug != "flynn-deploy" || fake.cfg.WebhookSecret != "s" {
		t.Fatalf("%+v", fake.cfg)
	}
	if err := runGitHubDisable(fake); err != nil {
		t.Fatal(err)
	}
	if fake.cfg.AppID != 0 {
		t.Fatalf("disable %+v", fake.cfg)
	}
}

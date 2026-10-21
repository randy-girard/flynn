package main

import (
	"strings"
	"testing"

	cfg "github.com/randy-girard/flynn/cli/config"
	ct "github.com/randy-girard/flynn/controller/types"
)

func TestGitPreviewURLAndAppFromGitURL(t *testing.T) {
	conf := &cfg.Cluster{GitURL: "https://git.example.com"}
	if got := gitPreviewURL(conf, "shop"); got != "https://git.example.com/_preview/shop.git" {
		t.Fatalf("preview url %q", got)
	}
	prev := config
	config = &cfg.Config{Clusters: []*cfg.Cluster{conf}}
	t.Cleanup(func() { config = prev })

	if app := appFromGitURL("https://git.example.com/_preview/shop.git"); app != nil {
		t.Fatalf("preview remote must not resolve as an app: %+v", app)
	}
	if app := appFromGitURL("https://git.example.com/myapp.git"); app == nil || app.Name != "myapp" {
		t.Fatalf("app remote %+v", app)
	}
}

func TestPreviewRemoteURL(t *testing.T) {
	conf := &cfg.Cluster{GitURL: "https://git.example.com"}
	app := &ct.App{Name: "myapp", Meta: map[string]string{
		appMetaPipeline:      "shop",
		appMetaPreviewRemote: "true",
	}}
	got, err := previewRemoteURL(conf, app)
	if err != nil || got != "https://git.example.com/_preview/shop.git" {
		t.Fatalf("got %q %v", got, err)
	}

	app.Meta[appMetaPreviewRemote] = "false"
	if _, err := previewRemoteURL(conf, app); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disabled: %v", err)
	}

	delete(app.Meta, appMetaPipeline)
	if _, err := previewRemoteURL(conf, app); err == nil || !strings.Contains(err.Error(), "not in a pipeline") {
		t.Fatalf("no pipeline: %v", err)
	}
}

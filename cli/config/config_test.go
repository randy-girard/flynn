package config

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v1controller "github.com/randy-girard/flynn/controller/client/v1"
	"golang.org/x/oauth2"
)

func TestDefaultPathHonorsFLYNNRC(t *testing.T) {
	t.Setenv("FLYNNRC", "/tmp/custom.flynnrc")
	if DefaultPath() != "/tmp/custom.flynnrc" {
		t.Fatalf("%s", DefaultPath())
	}
}

func TestConfigAddRemoveDefaultAndConflicts(t *testing.T) {
	c := &Config{}
	a := &Cluster{Name: "prod", ControllerURL: "https://controller.prod", GitURL: "https://git.prod", DockerPushURL: "https://docker.prod"}
	if err := c.Add(a, false); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(&Cluster{Name: "prod", ControllerURL: "https://other"}, false); err == nil {
		t.Fatal("duplicate name")
	}
	if err := c.Add(&Cluster{Name: "other", ControllerURL: a.ControllerURL}, false); err == nil {
		t.Fatal("duplicate controller URL")
	}
	if err := c.Add(&Cluster{Name: "other", ControllerURL: "https://x", GitURL: a.GitURL}, false); err == nil {
		t.Fatal("duplicate git URL")
	}
	if err := c.Add(&Cluster{Name: "other", ControllerURL: "https://x", DockerPushURL: a.DockerPushURL}, false); err == nil {
		t.Fatal("duplicate docker URL")
	}

	if err := c.Add(&Cluster{Name: "prod", ControllerURL: "https://controller.stg"}, true); err != nil {
		t.Fatal(err)
	}
	if len(c.Clusters) != 1 || c.Clusters[0].ControllerURL != "https://controller.stg" {
		t.Fatalf("force replace: %+v", c.Clusters)
	}

	if !c.SetDefault("prod") || c.Default != "prod" {
		t.Fatal("set default")
	}
	if c.SetDefault("missing") {
		t.Fatal("missing default")
	}
	if got := c.Remove("prod"); got == nil || got.Name != "prod" || len(c.Clusters) != 0 {
		t.Fatalf("remove: %+v %d", got, len(c.Clusters))
	}
	if c.Remove("prod") != nil {
		t.Fatal("second remove")
	}
}

func TestReadWriteFlynnrc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flynnrc")
	c := &Config{Default: "prod", Clusters: []*Cluster{{
		Name:          "prod",
		Key:           "cluster-secret",
		ControllerURL: "https://controller.example",
		GitURL:        "https://git.example",
		TLSPin:        "abc",
	}}}
	if err := c.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != "prod" || len(got.Clusters) != 1 {
		t.Fatalf("%+v", got)
	}
	if got.Clusters[0].Key != "cluster-secret" || got.Clusters[0].TLSPin != "abc" {
		t.Fatalf("secrets: %+v", got.Clusters[0])
	}
}

func TestSaveToRestrictsFlynnrcMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SEC-022 file mode is a POSIX permission concern")
	}
	c := &Config{Default: "prod", Clusters: []*Cluster{{
		Name:          "prod",
		Key:           "cluster-secret",
		ControllerURL: "https://controller.example",
	}}}

	t.Run("new file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flynnrc")
		if err := c.SaveTo(path); err != nil {
			t.Fatal(err)
		}
		assertFileMode(t, path, 0600)
	})

	t.Run("existing world-readable file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flynnrc")
		if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}
		// WriteFile honors umask; force the inherited 0644 the finding describes.
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0644 {
			t.Fatalf("precondition: got %o", st.Mode().Perm())
		}
		if err := c.SaveTo(path); err != nil {
			t.Fatal(err)
		}
		assertFileMode(t, path, 0600)
	})
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("%s mode %o, want %o", path, got, want)
	}
}

func TestDockerPushHostAndTLSPin(t *testing.T) {
	c := &Cluster{}
	if _, err := c.DockerPushHost(); err != ErrNoDockerPushURL {
		t.Fatalf("got %v", err)
	}
	c.DockerPushURL = "https://docker.example:5000/v2/"
	host, err := c.DockerPushHost()
	if err != nil || host != "docker.example:5000" {
		t.Fatalf("%q %v", host, err)
	}

	c.Name = "prod"
	c.ControllerURL = "https://controller.example"
	if _, err := c.Client(); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("missing login: %v", err)
	}
	c.Key = "flynn_pat_abc"
	c.TLSPin = "not-base64!!!"
	if _, err := c.Client(); err == nil || !strings.Contains(err.Error(), "tls pin") {
		t.Fatalf("bad pin: %v", err)
	}
	if _, err := c.TarClient(); err == nil {
		t.Fatal("missing ImageURL")
	}
	c.ImageURL = "https://tar.example"
	if _, err := c.TarClient(); err == nil || !strings.Contains(err.Error(), "tls pin") {
		t.Fatalf("tar pin: %v", err)
	}
}

func TestCACertPath(t *testing.T) {
	p := CACertPath("prod")
	if !strings.HasSuffix(p, filepath.Join("ca-certs", "prod.pem")) {
		t.Fatalf("%s", p)
	}
}

func TestCAClientOmitsKey(t *testing.T) {
	c := &Cluster{
		ControllerURL: "https://controller.example",
		Key:           "cluster-secret",
	}
	ca, err := c.CAClient()
	if err != nil {
		t.Fatal(err)
	}
	v1, ok := ca.(*v1controller.Client)
	if !ok {
		t.Fatalf("%T", ca)
	}
	if v1.Key != "" {
		t.Fatalf("CAClient must not send the cluster key, got %q", v1.Key)
	}
	if _, err := c.Client(); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("cluster key must not authenticate Client(): %v", err)
	}
	c.Key = "flynn_pat_abc"
	cli, err := c.Client()
	if err != nil {
		t.Fatal(err)
	}
	v1c, ok := cli.(*v1controller.Client)
	if !ok {
		t.Fatalf("%T", cli)
	}
	if v1c.Key != "flynn_pat_abc" {
		t.Fatalf("Client key=%q", v1c.Key)
	}
	if _, err := (&Cluster{ControllerURL: "https://c", TLSPin: "not-base64"}).CAClient(); err == nil {
		t.Fatal("invalid pin must fail")
	}
}

func TestClientUsesPerClusterCachedTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	issued := time.Now().UTC()
	makeTok := func(access, audience string) *oauth2.Token {
		return (&oauth2.Token{
			AccessToken:  access,
			RefreshToken: "r-" + access,
			TokenType:    "Bearer",
			Expiry:       issued.Add(time.Hour),
		}).WithExtra(map[string]interface{}{
			"refresh_token_expiry":     issued.Add(24 * time.Hour),
			"refresh_token_issue_time": issued,
			"audience":                 audience,
		})
	}
	cache := TokenCache()
	if err := cache.SetToken("alpha", "flynn-cli", makeTok("a-tok", "https://controller.a")); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetToken("beta", "flynn-cli", makeTok("b-tok", "https://controller.b")); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Cluster{Name: "alpha", ControllerURL: "https://controller.a"}).Client(); err != nil {
		t.Fatalf("alpha: %v", err)
	}
	if _, err := (&Cluster{Name: "beta", ControllerURL: "https://controller.b"}).Client(); err != nil {
		t.Fatalf("beta: %v", err)
	}
	if _, err := (&Cluster{Name: "gamma", ControllerURL: "https://controller.g"}).Client(); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("gamma must require login: %v", err)
	}
}

func TestHTTPClientUsesTLSPin(t *testing.T) {
	hc, err := (&Cluster{}).HTTPClient()
	if err != nil || hc != http.DefaultClient {
		t.Fatalf("no pin: %v %v", hc, err)
	}
	c := &Cluster{TLSPin: "not-base64!!!", ControllerURL: "https://controller.example"}
	if _, err := c.HTTPClient(); err == nil || !strings.Contains(err.Error(), "tls pin") {
		t.Fatalf("bad pin: %v", err)
	}
	c.TLSPin = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	hc, err = c.HTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if hc == http.DefaultClient {
		t.Fatal("pin must not use DefaultClient")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	c := &Config{Default: "a", Clusters: []*Cluster{{Name: "a", ControllerURL: "https://c"}}}
	body := c.Marshal()
	if !strings.Contains(string(body), `Name = "a"`) {
		t.Fatalf("%s", body)
	}
	empty := &Config{}
	path := filepath.Join(t.TempDir(), "empty")
	if err := empty.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() != 0 {
		t.Fatalf("empty config must write an empty file: %v %v", st, err)
	}
}

func TestAuthURL(t *testing.T) {
	if got := AuthURL("demo.localflynn.com"); got != "https://auth.demo.localflynn.com" {
		t.Fatalf("%s", got)
	}
	if AuthURL("  ") != "" || AuthURL("") != "" {
		t.Fatal("empty domain")
	}
	if got := AuthURLFromController("https://controller.demo.localflynn.com"); got != "https://auth.demo.localflynn.com" {
		t.Fatalf("%s", got)
	}
	if got := AuthURLFromController("https://auth.demo.localflynn.com"); got != "https://auth.demo.localflynn.com" {
		t.Fatalf("passthrough %s", got)
	}
}

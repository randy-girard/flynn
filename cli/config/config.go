package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/mitchellh/go-homedir"
	"github.com/randy-girard/flynn/cli/login/tokensource"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/pkg/pinned"
	tarclient "github.com/randy-girard/flynn/tarreceive/client"
	"golang.org/x/oauth2"
)

var ErrNoDockerPushURL = errors.New("ERROR: Docker push URL not configured, set it with 'flynn docker set-push-url'")

type Cluster struct {
	Name          string `json:"name"`
	OAuthURL      string `json:"oauth_url,omitempty" toml:"OAuthURL,omitempty"`
	DashboardURL  string `json:"dashboard_url,omitempty" toml:"DashboardURL,omitempty"`
	Key           string `json:"key,omitempty" toml:"Key,omitempty"`
	TLSPin        string `json:"tls_pin" toml:"TLSPin,omitempty"`
	ControllerURL string `json:"controller_url"`
	GitURL        string `json:"git_url"`
	ImageURL      string `json:"image_url"`
	DockerPushURL string `json:"docker_push_url,omitempty" toml:"DockerPushURL,omitempty"`
	// Context is the default owner handle for create and collaborator commands.
	// It is not an access check.
	Context string `json:"context,omitempty" toml:"Context,omitempty"`
}

func (c *Cluster) pinnedControllerConfig() (controller.Config, error) {
	var pin []byte
	if c.TLSPin != "" {
		var err error
		pin, err = base64.StdEncoding.DecodeString(c.TLSPin)
		if err != nil {
			return controller.Config{}, fmt.Errorf("error decoding tls pin: %s", err)
		}
	}
	return controller.Config{Pin: pin}, nil
}

func IsPersonalAccessToken(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), "flynn_pat_")
}

func (c *Cluster) issuerURL() string {
	if strings.TrimSpace(c.OAuthURL) != "" {
		return strings.TrimSpace(c.OAuthURL)
	}
	return strings.TrimSpace(c.ControllerURL)
}

// HTTPClient returns an HTTP client that trusts this cluster's TLS pin, the
// same way controller API calls do. Login and token refresh must use this;
// macOS rejects the Flynn CA as "not standards compliant" on DefaultClient.
func (c *Cluster) HTTPClient() (*http.Client, error) {
	if c == nil || strings.TrimSpace(c.TLSPin) == "" {
		return http.DefaultClient, nil
	}
	pin, err := base64.StdEncoding.DecodeString(c.TLSPin)
	if err != nil {
		return nil, fmt.Errorf("error decoding tls pin: %s", err)
	}
	d := &pinned.Config{Pin: pin}
	return &http.Client{Transport: &http.Transport{DialTLS: d.DialOnce}}, nil
}

func hostnameFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// AuthURL is the cluster login issuer (OAuth), https://auth.<domain>.
func AuthURL(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	return "https://auth." + domain
}

// AuthURLFromController maps https://controller.<domain> to https://auth.<domain>.
func AuthURLFromController(controllerURL string) string {
	host := hostnameFromURL(controllerURL)
	if strings.HasPrefix(host, "controller.") {
		return "https://auth." + strings.TrimPrefix(host, "controller.")
	}
	if host != "" {
		return "https://" + host
	}
	return strings.TrimSpace(controllerURL)
}

func (c *Cluster) oauthContext() (context.Context, *http.Client, error) {
	hc, err := c.HTTPClient()
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(context.Background(), oauth2.HTTPClient, hc), hc, nil
}

func (c *Cluster) notLoggedIn() error {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = c.ControllerURL
	}
	return fmt.Errorf("not logged in to cluster %q; run flynn login", name)
}

func (c *Cluster) Client() (controller.Client, error) {
	if IsPersonalAccessToken(c.Key) {
		cfg, err := c.pinnedControllerConfig()
		if err != nil {
			return nil, err
		}
		return controller.NewClientWithConfig(c.ControllerURL, c.Key, cfg)
	}
	issuer := c.issuerURL()
	if issuer == "" {
		return nil, c.notLoggedIn()
	}
	ctx, hc, err := c.oauthContext()
	if err != nil {
		return nil, err
	}
	ts, err := tokensource.New(c.Name, issuer, c.ControllerURL, TokenCache(), hc)
	if err != nil {
		if errors.Is(err, tokensource.ErrTokenNotFound) {
			return nil, c.notLoggedIn()
		}
		return nil, err
	}
	return controller.NewClientWithHTTP(c.ControllerURL, "", oauth2.NewClient(ctx, ts))
}

// CAClient is an unauthenticated controller client for GET /ca-cert. That
// route is TOFU: sending a wrong cluster key 401s on older controllers even
// though the same request with no credentials succeeds.
func (c *Cluster) CAClient() (controller.Client, error) {
	cfg, err := c.pinnedControllerConfig()
	if err != nil {
		return nil, err
	}
	return controller.NewClientWithConfig(c.ControllerURL, "", cfg)
}

func (c *Cluster) TarClient() (*tarclient.Client, error) {
	if c.ImageURL == "" {
		return nil, errors.New("cluster: missing ImageURL .flynnrc config")
	}
	if IsPersonalAccessToken(c.Key) {
		var pin []byte
		if c.TLSPin != "" {
			var err error
			pin, err = base64.StdEncoding.DecodeString(c.TLSPin)
			if err != nil {
				return nil, fmt.Errorf("error decoding tls pin: %s", err)
			}
		}
		return tarclient.NewClientWithConfig(c.ImageURL, c.Key, tarclient.Config{Pin: pin}), nil
	}
	issuer := c.issuerURL()
	if issuer == "" {
		return nil, c.notLoggedIn()
	}
	ctx, hc, err := c.oauthContext()
	if err != nil {
		return nil, err
	}
	ts, err := tokensource.New(c.Name, issuer, c.ControllerURL, TokenCache(), hc)
	if err != nil {
		if errors.Is(err, tokensource.ErrTokenNotFound) {
			return nil, c.notLoggedIn()
		}
		return nil, err
	}
	return tarclient.NewClientWithHTTP(c.ImageURL, oauth2.NewClient(ctx, ts)), nil
}

func (c *Cluster) DockerPushHost() (string, error) {
	if c.DockerPushURL == "" {
		return "", ErrNoDockerPushURL
	}
	u, err := url.Parse(c.DockerPushURL)
	if err != nil {
		return "", fmt.Errorf("cluster: could not parse DockerPushURL: %s", err)
	}
	return u.Host, nil
}

type Config struct {
	Default  string     `toml:"default"`
	Clusters []*Cluster `toml:"cluster"`
}

func HomeDir() string {
	dir, err := homedir.Dir()
	if err != nil {
		panic(err)
	}
	return dir
}

func Dir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "flynn")
	}
	return filepath.Join(HomeDir(), ".flynn")
}

func DefaultPath() string {
	if p := os.Getenv("FLYNNRC"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(Dir(), "flynnrc")
	}
	return filepath.Join(HomeDir(), ".flynnrc")
}

func TokenCache() tokensource.Cache {
	return tokensource.NewTokenCache(filepath.Join(Dir(), "tokens"))
}

func ReadFile(path string) (*Config, error) {
	c := &Config{}
	_, err := toml.DecodeFile(path, c)
	if err != nil {
		return c, err
	}
	return c, nil
}

func (c *Config) Marshal() []byte {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func (c *Config) Add(s *Cluster, force bool) error {
	var msg string
	conflictIdx := -1
	for i, existing := range c.Clusters {
		var m string
		switch {
		case existing.Name == s.Name:
			m = fmt.Sprintf("Cluster %q already exists in ~/.flynnrc", s.Name)
		case existing.GitURL != "" && existing.GitURL == s.GitURL:
			m = fmt.Sprintf("A cluster with the URL %q already exists in ~/.flynnrc", s.GitURL)
		case existing.ControllerURL == s.ControllerURL:
			m = fmt.Sprintf("A cluster with the URL %q already exists in ~/.flynnrc", s.ControllerURL)
		case existing.DockerPushURL != "" && existing.DockerPushURL == s.DockerPushURL:
			m = fmt.Sprintf("A cluster with the URL %q already exists in ~/.flynnrc", s.DockerPushURL)
		}
		if m != "" {
			if conflictIdx != -1 && conflictIdx != i {
				return fmt.Errorf("The cluster name and/or URLs conflict with multiple existing clusters.")
			}
			conflictIdx = i
			msg = m
		}
	}

	// The new cluster config conflicts with an existing one
	if msg != "" {
		if !force {
			return fmt.Errorf("%s", msg)
		}

		// Remove conflicting cluster
		c.Clusters = append(c.Clusters[:conflictIdx], c.Clusters[conflictIdx+1:]...)
	}

	c.Clusters = append(c.Clusters, s)

	return nil
}

func (c *Config) Upgrade() (changed bool) {
	// Any "config migrations" should be done in this function
	return false
}

func (c *Config) Remove(name string) *Cluster {
	for i, s := range c.Clusters {
		if s.Name != name {
			continue
		}
		c.Clusters = append(c.Clusters[:i], c.Clusters[i+1:]...)
		return s
	}
	return nil
}

func (c *Config) SetDefault(name string) bool {
	for _, s := range c.Clusters {
		if s.Name != name {
			continue
		}
		c.Default = name
		return true
	}
	return false
}

func (c *Config) SaveTo(path string) error {
	// SEC-022: ~/.flynnrc holds TLS pins and optional PATs; never inherit the process umask
	// (os.Create is 0666 → typically 0644). OpenFile 0600 covers new files;
	// chmod tightens an existing world-readable config on the next save.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}

	if len(c.Clusters) != 0 {
		if err := toml.NewEncoder(f).Encode(c); err != nil {
			return err
		}
		f.Write([]byte("\n"))
	}
	return nil
}

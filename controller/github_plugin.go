package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func (c *controllerAPI) githubPluginBase() string {
	if c != nil && strings.TrimSpace(c.githubPluginURL) != "" {
		return strings.TrimRight(c.githubPluginURL, "/")
	}
	if u := strings.TrimSpace(os.Getenv("GITHUB_PLUGIN_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://github.discoverd"
}

var (
	githubPluginMu      sync.Mutex
	githubPluginOK      bool
	githubPluginChecked time.Time
)

func (c *controllerAPI) githubPluginHealthy() bool {
	base := c.githubPluginBase()
	if base == "" || (c.githubPluginURL == "" && strings.TrimSpace(os.Getenv("GITHUB_PLUGIN_URL")) == "") {
		return false
	}
	now := time.Now()
	githubPluginMu.Lock()
	if now.Sub(githubPluginChecked) < 2*time.Second {
		ok := githubPluginOK
		githubPluginMu.Unlock()
		return ok
	}
	githubPluginMu.Unlock()

	client := c.githubHTTP
	if client == nil {
		client = &http.Client{Timeout: 400 * time.Millisecond}
	}
	req, err := http.NewRequest(http.MethodGet, base+"/.well-known/status", nil)
	if err != nil {
		c.setGitHubPluginHealth(false)
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		c.setGitHubPluginHealth(false)
		return false
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	ok := res.StatusCode == 200
	c.setGitHubPluginHealth(ok)
	return ok
}

func (c *controllerAPI) setGitHubPluginHealth(ok bool) {
	githubPluginMu.Lock()
	githubPluginOK = ok
	githubPluginChecked = time.Now()
	githubPluginMu.Unlock()
}

func (c *controllerAPI) maybeGitHubPlugin(next httphelper.HandlerFunc) httphelper.HandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, req *http.Request) {
		if c.proxyGitHubPlugin(w, req) {
			return
		}
		next(ctx, w, req)
	}
}

func (c *controllerAPI) proxyGitHubPlugin(w http.ResponseWriter, req *http.Request) bool {
	if !c.githubPluginHealthy() {
		return false
	}
	base := c.githubPluginBase()
	url := base + req.URL.RequestURI()
	body := req.Body
	out, err := http.NewRequest(req.Method, url, body)
	if err != nil {
		return false
	}
	out.Header = req.Header.Clone()
	if key := firstNonEmpty(os.Getenv("AUTH_KEY"), os.Getenv("CONTROLLER_KEY")); key != "" {
		out.SetBasicAuth("", strings.Split(key, ",")[0])
	}
	client := c.githubHTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(out)
	if err != nil {
		c.setGitHubPluginHealth(false)
		return false
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
	return true
}

func (c *controllerAPI) ExportGitHub(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if c.githubStore == nil {
		httphelper.JSON(w, 200, map[string]interface{}{"config": &ct.GitHubAppConfig{}, "connections": []*ct.GitHubRepoConnection{}})
		return
	}
	cfg, err := c.githubStore.GetConfig()
	if err != nil {
		respondWithError(w, err)
		return
	}
	conns, err := c.githubStore.ListAll()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, map[string]interface{}{"config": cfg, "connections": conns})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

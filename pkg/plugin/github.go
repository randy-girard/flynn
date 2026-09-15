package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	ct "github.com/flynn/flynn/controller/types"
)

const (
	githubUserAgent = "flynn-host-plugin"
	githubTimeout   = 5 * time.Minute
)

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (r githubRelease) asset(name string) *githubAsset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func (in *Installer) githubHTTP() *http.Client {
	if in.GitHubHTTP != nil {
		return in.GitHubHTTP
	}
	return &http.Client{Timeout: githubTimeout}
}

// fetchGitHub writes flynn-plugin.json and dist/ (image.json + layers) from a
// GitHub Release into a temp directory. It never runs plugin-build.
func (in *Installer) fetchGitHub(src *GitHubSource, credsFile string) (string, error) {
	if src == nil {
		return "", fmt.Errorf("missing GitHub source")
	}
	token, api, err := TokenForHost(src.Host, credsFile)
	if err != nil {
		return "", err
	}
	if api != "" {
		src.API = api
	}
	if src.API == "" {
		src.API = "https://api.github.com"
	}

	rel, err := in.getRelease(src, token)
	if err != nil {
		return "", err
	}
	if src.Ref == "" || src.Ref == "latest" {
		src.Ref = rel.TagName
	}

	root, err := os.MkdirTemp("", "flynn-plugin-"+src.Repo+"-*")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(root)
		}
	}()

	if err := os.MkdirAll(filepath.Join(root, DistDir), 0755); err != nil {
		return "", err
	}

	if a := rel.asset(ManifestName); a != nil {
		if err := in.downloadAsset(src, token, a, filepath.Join(root, ManifestName)); err != nil {
			return "", fmt.Errorf("%s: %w", ManifestName, err)
		}
	} else {
		return "", fmt.Errorf("GitHub release %s/%s@%s has no %s asset (include it in Build and Release)", src.Owner, src.Repo, rel.TagName, ManifestName)
	}

	imagePath := filepath.Join(root, DistDir, ImageJSON)
	if a := rel.asset(ImageJSON); a != nil {
		if err := in.downloadAsset(src, token, a, imagePath); err != nil {
			return "", fmt.Errorf("%s: %w", ImageJSON, err)
		}
	} else if u := src.ReleaseAssetURL(ImageJSON); u != "" {
		if err := in.downloadURL(token, u, imagePath); err != nil {
			return "", fmt.Errorf("%s: %w", ImageJSON, err)
		}
	} else {
		return "", fmt.Errorf("GitHub release %s/%s@%s has no %s; publish the plugin image or pass --ref", src.Owner, src.Repo, rel.TagName, ImageJSON)
	}

	art := &ct.Artifact{}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, art); err != nil {
		return "", fmt.Errorf("parse %s: %w", ImageJSON, err)
	}
	if err := ValidatePluginLayers(art); err != nil {
		return "", fmt.Errorf("GitHub release %s/%s@%s image.json: %w", src.Owner, src.Repo, rel.TagName, err)
	}

	for _, layer := range layers(art) {
		name := layer.ID + ".squashfs"
		dest := filepath.Join(root, DistDir, name)
		if a := rel.asset(name); a != nil {
			if err := in.downloadAsset(src, token, a, dest); err != nil {
				return "", fmt.Errorf("layer %s: %w", layer.ID, err)
			}
			continue
		}
		if u := art.LayerURL(layer); u != "" {
			if err := in.downloadURL(token, u, dest); err != nil {
				return "", fmt.Errorf("layer %s: %w", layer.ID, err)
			}
			continue
		}
		return "", fmt.Errorf("layer %s not in release %s", layer.ID, rel.TagName)
	}

	in.logf("pulled plugin image from GitHub %s/%s@%s", src.Owner, src.Repo, rel.TagName)
	ok = true
	return root, nil
}

func (in *Installer) getRelease(src *GitHubSource, token string) (*githubRelease, error) {
	api := strings.TrimRight(src.API, "/")
	var u string
	ref := src.Ref
	if ref == "" || ref == "latest" {
		u = fmt.Sprintf("%s/repos/%s/%s/releases/latest", api, src.Owner, src.Repo)
	} else {
		u = fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", api, src.Owner, src.Repo, url.PathEscape(ref))
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	in.githubHeaders(req, token, "application/vnd.github+json")
	res, err := in.githubHTTP().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub release %s/%s: %w", src.Owner, src.Repo, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		hint := "check --ref and that the release is published"
		if token == "" && (res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnauthorized) {
			hint = "for private or draft releases set credentials: flynn-host plugin credentials set github"
		}
		return nil, fmt.Errorf("GitHub release %s/%s@%s: %s (%s)", src.Owner, src.Repo, refOrLatest(ref), res.Status, hint)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub release %s/%s: %s", src.Owner, src.Repo, res.Status)
	}
	rel := &githubRelease{}
	if err := json.Unmarshal(body, rel); err != nil {
		return nil, fmt.Errorf("decode GitHub release: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("GitHub release %s/%s missing tag_name", src.Owner, src.Repo)
	}
	return rel, nil
}

func (in *Installer) downloadAsset(_ *GitHubSource, token string, a *githubAsset, dest string) error {
	if a == nil {
		return fmt.Errorf("missing asset")
	}
	if token != "" && a.URL != "" {
		return in.downloadURLAuth(token, a.URL, dest, "application/octet-stream")
	}
	if a.BrowserDownloadURL != "" {
		return in.downloadURL(token, a.BrowserDownloadURL, dest)
	}
	if a.URL != "" {
		return in.downloadURLAuth(token, a.URL, dest, "application/octet-stream")
	}
	return fmt.Errorf("asset %s has no download URL", a.Name)
}

func (in *Installer) downloadURL(token, rawURL, dest string) error {
	accept := ""
	if token != "" {
		accept = "application/octet-stream"
	}
	return in.downloadURLAuth(token, rawURL, dest, accept)
}

func (in *Installer) downloadURLAuth(token, rawURL, dest, accept string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	in.githubHeaders(req, token, accept)
	res, err := in.githubHTTP().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", sanitizeURL(rawURL), res.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, res.Body); err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}

func (in *Installer) githubHeaders(req *http.Request, token, accept string) {
	req.Header.Set("User-Agent", githubUserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func refOrLatest(ref string) string {
	if ref == "" {
		return "latest"
	}
	return ref
}

func sanitizeURL(raw string) string {
	if i := strings.Index(raw, "@"); i >= 0 && strings.Contains(raw, "://") {
		return strings.SplitN(raw, "://", 2)[0] + "://***"
	}
	return raw
}

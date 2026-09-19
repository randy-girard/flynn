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

	ct "github.com/randy-girard/flynn/controller/types"
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

// fetchGitHub writes flynn-plugin.json, declared hook scripts, and dist/
// (image.json + layers) from a GitHub Release into a temp directory. It never
// runs plugin-build. GitHub assets are a flat list, so a declared hook path
// such as script/install.sh or script/uninstall.sh is published as
// script-install.sh / script-uninstall.sh / script-ready.sh (basename is also accepted).
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

	if err := in.fetchGitHubHooks(root, src, token, rel); err != nil {
		return "", err
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

	ls := layers(art)
	for i, layer := range ls {
		if layer == nil || layer.ID == "" {
			return "", fmt.Errorf("image.json layer %d missing id", i)
		}
		name := layer.ID + ".squashfs"
		dest := filepath.Join(root, DistDir, name)
		if a := rel.asset(name); a != nil {
			if err := in.downloadAsset(src, token, a, dest); err != nil {
				return "", fmt.Errorf("layer %s: %w", layer.ID, err)
			}
			continue
		}
		// Flynn ubuntu-noble is already on the Flynn GitHub Release (same
		// layer id). Plugin jobs only publish the overlay delta so N plugins
		// do not re-upload ~200MiB in parallel (uploads.github.com 5xx).
		if i < len(ls)-1 {
			if u := flynnBaseLayerURL(art, name); u != "" {
				if err := in.downloadURL(token, u, dest); err != nil {
					return "", fmt.Errorf("layer %s from Flynn %s: %w", layer.ID, art.Meta["flynn.plugin.base"], err)
				}
				continue
			}
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

func (m *Manifest) hookRels() []string {
	if m == nil || m.Hooks == nil {
		return nil
	}
	var out []string
	for _, rel := range []string{m.Hooks.Install, m.Hooks.Upgrade, m.Hooks.Uninstall, m.Hooks.Ready} {
		rel = strings.TrimSpace(rel)
		if rel != "" {
			out = append(out, rel)
		}
	}
	return out
}

// HookAssetNames are GitHub Release asset names for a repo-relative hook path.
// Assets cannot contain slashes, so script/install.sh is published as
// script-install.sh (script/uninstall.sh as script-uninstall.sh); the
// basename is also accepted.
func HookAssetNames(rel string) []string {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return nil
	}
	flat := strings.ReplaceAll(rel, "/", "-")
	base := filepath.Base(rel)
	names := []string{flat}
	if base != "" && base != flat {
		names = append(names, base)
	}
	return names
}

func pluginRelPath(root, rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("invalid hook path %q", rel)
	}
	dest := filepath.Join(root, filepath.FromSlash(rel))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	sep := string(os.PathSeparator)
	if absDest != absRoot && !strings.HasPrefix(absDest, absRoot+sep) {
		return "", fmt.Errorf("hook path %q escapes plugin unpack", rel)
	}
	return dest, nil
}

func (in *Installer) fetchGitHubHooks(root string, src *GitHubSource, token string, rel *githubRelease) error {
	m, err := LoadManifest(root)
	if err != nil {
		return err
	}
	for _, hookRel := range m.hookRels() {
		dest, err := pluginRelPath(root, hookRel)
		if err != nil {
			return fmt.Errorf("hooks %s: %w", hookRel, err)
		}
		names := HookAssetNames(hookRel)
		if len(names) == 0 {
			return fmt.Errorf("hooks %s: invalid path", hookRel)
		}
		var a *githubAsset
		for _, name := range names {
			if a = rel.asset(name); a != nil {
				break
			}
		}
		if a == nil {
			return fmt.Errorf("GitHub release %s/%s@%s has no hooks asset for %s (publish %s in Build and Release)", src.Owner, src.Repo, rel.TagName, hookRel, strings.Join(names, " or "))
		}
		if err := in.downloadAsset(src, token, a, dest); err != nil {
			return fmt.Errorf("hooks %s: %w", hookRel, err)
		}
		if err := os.Chmod(dest, 0755); err != nil {
			return fmt.Errorf("hooks %s: %w", hookRel, err)
		}
		in.logf("pulled hook %s from GitHub release assets", hookRel)
	}
	return nil
}

func (in *Installer) getRelease(src *GitHubSource, token string) (*githubRelease, error) {
	ref := src.Ref
	if ref == "" || ref == "latest" {
		if rel, err := in.latestCalVerRelease(src, token); err == nil {
			return rel, nil
		}
		return in.fetchReleaseJSON(src, token, in.githubAPI(src)+fmt.Sprintf("/repos/%s/%s/releases/latest", src.Owner, src.Repo), refOrLatest(ref))
	}
	return in.fetchReleaseJSON(src, token, in.githubAPI(src)+fmt.Sprintf("/repos/%s/%s/releases/tags/%s", src.Owner, src.Repo, url.PathEscape(ref)), ref)
}

func (in *Installer) githubAPI(src *GitHubSource) string {
	return strings.TrimRight(src.API, "/")
}

// latestCalVerRelease picks the highest vYYYYMMDD.N.P (or legacy vYYYYMMDD.N)
// among published, non-prerelease GitHub Releases so plugin:update without
// --ref follows plugin-only patches, not whichever tag GitHub marked latest.
func (in *Installer) latestCalVerRelease(src *GitHubSource, token string) (*githubRelease, error) {
	all, err := in.listGitHubReleases(src, token)
	if err != nil {
		return nil, err
	}
	var best *githubRelease
	for i := range all {
		r := &all[i]
		if r.Draft || r.Prerelease || r.TagName == "" {
			continue
		}
		if _, ok := ParsePluginCalVer(r.TagName); !ok {
			continue
		}
		if best == nil || ComparePluginCalVer(r.TagName, best.TagName) > 0 {
			best = r
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no published plugin calver releases")
	}
	return best, nil
}

func (in *Installer) listGitHubReleases(src *GitHubSource, token string) ([]githubRelease, error) {
	u := in.githubAPI(src) + fmt.Sprintf("/repos/%s/%s/releases?per_page=100", src.Owner, src.Repo)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	in.githubHeaders(req, token, "application/vnd.github+json")
	res, err := in.githubHTTP().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub releases %s/%s: %w", src.Owner, src.Repo, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases %s/%s: %s", src.Owner, src.Repo, res.Status)
	}
	var all []githubRelease
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, fmt.Errorf("decode GitHub releases: %w", err)
	}
	return all, nil
}

func (in *Installer) fetchReleaseJSON(src *GitHubSource, token, u, refLabel string) (*githubRelease, error) {
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
			hint = "for private or draft releases set credentials: flynn-host plugin:credentials-set github"
		}
		return nil, fmt.Errorf("GitHub release %s/%s@%s: %s (%s)", src.Owner, src.Repo, refLabel, res.Status, hint)
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

// githubBrowserDownloadURL builds a public GitHub Releases download URL.
// Tests replace this to point at httptest.
var githubBrowserDownloadURL = func(repo, tag, name string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, name)
}

// parsePluginBase reads artifact meta flynn.plugin.base ("owner/repo@version").
func parsePluginBase(meta map[string]string) (repo, version string, ok bool) {
	if meta == nil {
		return "", "", false
	}
	raw := strings.TrimSpace(meta["flynn.plugin.base"])
	i := strings.LastIndex(raw, "@")
	if i <= 0 || i >= len(raw)-1 {
		return "", "", false
	}
	repo = strings.TrimSpace(raw[:i])
	version = strings.TrimSpace(raw[i+1:])
	if repo == "" || version == "" || version == "latest" {
		return "", "", false
	}
	if strings.Count(repo, "/") != 1 {
		return "", "", false
	}
	return repo, version, true
}

func flynnBaseLayerURL(art *ct.Artifact, name string) string {
	if art == nil || name == "" {
		return ""
	}
	repo, version, ok := parsePluginBase(art.Meta)
	if !ok {
		return ""
	}
	return githubBrowserDownloadURL(repo, version, name)
}

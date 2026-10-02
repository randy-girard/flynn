package plugin

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	// DefaultLayerCacheDir is where flynn-host download stores squashfs layers.
	DefaultLayerCacheDir = "/var/lib/flynn/layer-cache"
	EnvLayersDir         = "FLYNN_LAYERS_DIR"
	EnvImagesJSON        = "FLYNN_IMAGES_JSON"
)

var defaultImagesJSONPaths = []string{
	"/etc/flynn/images.json",
	"/etc/flynn/images.json.gz",
}

// extraImagesJSONPaths are GOPATH builder checkouts; tests nil this so a
// laptop/builder images.json cannot leak into FlynnSourceRoot isolation.
var extraImagesJSONPaths = []string{
	"/root/go/src/github.com/flynn/flynn/build/images.json",
	"/root/go/src/github.com/flynn/flynn/build/manifests/images.json",
	"/root/go/src/github.com/randy-girard/flynn/build/images.json",
	"/root/go/src/github.com/randy-girard/flynn/build/manifests/images.json",
}

// flynnSourceRootFile is written on Vagrant cluster nodes so plugin-build
// finds Flynn when flynn-host is /usr/bin (not inside the checkout).
var flynnSourceRootFile = "/etc/flynn/source-root"

// flynnSourceRootExecutable is os.Executable; GOPATH `go test` binaries live
// under the Flynn checkout, so tests replace this to hide that walk.
var flynnSourceRootExecutable = os.Executable

// defaultFlynnCheckouts are Vagrant / GOPATH layouts used when FLYNN_ROOT is unset.
var defaultFlynnCheckouts = []string{
	"/root/go/src/github.com/flynn/flynn",
	"/root/go/src/github.com/randy-girard/flynn",
	"/vagrant",
}

func localImagesJSONCandidates() []string {
	out := append([]string{}, defaultImagesJSONPaths...)
	if root := strings.TrimSpace(os.Getenv("FLYNN_ROOT")); root != "" {
		out = append([]string{
			filepath.Join(root, "build", "images.json"),
			filepath.Join(root, "build", "manifests", "images.json"),
		}, out...)
	}
	out = append(out, extraImagesJSONPaths...)
	return out
}

// FindLocalFlynnLayer returns a local squashfs for a Flynn OS/base layer id.
// Checks FLYNN_LAYERS_DIR, /var/lib/flynn/layer-cache, and files next to a
// local images.json (FLYNN_IMAGES_JSON or /etc/flynn/images.json).
func FindLocalFlynnLayer(id string) string {
	return findLocalFlynnLayer(id, localLayerSearchDirs())
}

func findLocalFlynnLayer(id string, dirs []string) string {
	if id == "" {
		return ""
	}
	name := id + ".squashfs"
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return p
		}
	}
	return ""
}

func localLayerSearchDirs() []string {
	var dirs []string
	if d := strings.TrimSpace(os.Getenv(EnvLayersDir)); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, DefaultLayerCacheDir)
	if img := localImagesJSONPath(); img != "" {
		base := filepath.Dir(img)
		dirs = append(dirs, base, filepath.Join(base, "layers"), filepath.Join(base, DistDir))
	}
	return dirs
}

// LocalImagesJSONPath is a cluster or build-tree images.json that lists Flynn
// artifacts. Empty when none is present.
func LocalImagesJSONPath() string {
	return localImagesJSONPath()
}

func localImagesJSONPath() string {
	if p := strings.TrimSpace(os.Getenv(EnvImagesJSON)); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, p := range localImagesJSONCandidates() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// FlynnSourceRoot is the Flynn checkout plugin-build should compile against
// (SEC-003 Auth-Key). Order: FLYNN_ROOT, /etc/flynn/source-root (Vagrant
// nodes), images.json next to this tree, walk up from the flynn-host binary
// or cwd, then well-known Vagrant checkouts.
func FlynnSourceRoot() string {
	if r := strings.TrimSpace(os.Getenv("FLYNN_ROOT")); r != "" && isFlynnModule(r) {
		return r
	}
	if r := flynnSourceRootFromFile(flynnSourceRootFile); r != "" {
		return r
	}
	if p := localImagesJSONPath(); p != "" {
		if r := flynnRootFromImagesJSON(p); r != "" {
			return r
		}
	}
	if exe, err := flynnSourceRootExecutable(); err == nil {
		if r := walkFlynnModule(filepath.Dir(exe)); r != "" {
			return r
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if r := walkFlynnModule(wd); r != "" {
			return r
		}
	}
	for _, p := range defaultFlynnCheckouts {
		if isFlynnModule(p) {
			return p
		}
	}
	return ""
}

func flynnSourceRootFromFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(b))
	if isFlynnModule(dir) {
		return dir
	}
	return ""
}

func walkFlynnModule(start string) string {
	dir := filepath.Clean(start)
	for i := 0; i < 8; i++ {
		if isFlynnModule(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func flynnRootFromImagesJSON(p string) string {
	dir := filepath.Dir(p)
	if filepath.Base(dir) == "manifests" {
		dir = filepath.Dir(dir)
	}
	if filepath.Base(dir) != "build" {
		return ""
	}
	root := filepath.Dir(dir)
	if isFlynnModule(root) {
		return root
	}
	return ""
}

func isFlynnModule(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		return line == "module github.com/randy-girard/flynn"
	}
	return false
}

// LocalFlynnImageEnv is FLYNN_IMAGES_JSON / FLYNN_LAYERS_DIR for plugin-build
// when this host already has Flynn images, so the builder does not re-download
// ubuntu-noble from GitHub.
func LocalFlynnImageEnv() []string {
	var env []string
	if p := localImagesJSONPath(); p != "" {
		env = append(env, EnvImagesJSON+"="+p)
	}
	if d := strings.TrimSpace(os.Getenv(EnvLayersDir)); d != "" {
		env = append(env, EnvLayersDir+"="+d)
	} else if st, err := os.Stat(DefaultLayerCacheDir); err == nil && st.IsDir() {
		env = append(env, EnvLayersDir+"="+DefaultLayerCacheDir)
	}
	return env
}

func imagesJSONHasLayer(path, id string) bool {
	if path == "" || id == "" {
		return false
	}
	arts, err := readImagesJSONArtifacts(path)
	if err != nil {
		return false
	}
	return artifactsHaveLayer(arts, id)
}

func artifactsHaveLayer(arts []*ct.Artifact, id string) bool {
	for _, a := range arts {
		for _, layer := range layers(a) {
			if layer != nil && layer.ID == id {
				return true
			}
		}
	}
	return false
}

func readImagesJSONArtifacts(path string) ([]*ct.Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	var images map[string]*ct.Artifact
	if err := json.NewDecoder(r).Decode(&images); err != nil {
		return nil, err
	}
	out := make([]*ct.Artifact, 0, len(images))
	for _, a := range images {
		if a != nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func (in *Installer) localFlynnLayer(id string) string {
	dirs := localLayerSearchDirs()
	if in != nil && strings.TrimSpace(in.LayerCacheDir) != "" {
		dirs = append([]string{strings.TrimSpace(in.LayerCacheDir)}, dirs...)
	}
	if p := findLocalFlynnLayer(id, dirs); p != "" {
		return p
	}
	return ""
}

func (in *Installer) clusterHasFlynnLayer(id string) bool {
	if id == "" {
		return false
	}
	if p := localImagesJSONPath(); p != "" && imagesJSONHasLayer(p, id) {
		return true
	}
	if in == nil || in.Client == nil {
		return false
	}
	arts, err := in.Client.ArtifactList()
	if err != nil {
		return false
	}
	return artifactsHaveLayer(arts, id)
}

// flynnLayerURL is an existing artifact LayerURL for this layer ID (images.json
// then controller artifacts). Prefer a non-plugin Flynn image so plugin jobs
// can fetch the OS squashfs without a second copy under the plugin prefix.
func (in *Installer) flynnLayerURL(id string) string {
	if id == "" {
		return ""
	}
	if p := localImagesJSONPath(); p != "" {
		arts, err := readImagesJSONArtifacts(p)
		if err == nil {
			if u := layerURLFromArtifacts(arts, id); u != "" {
				return u
			}
		}
	}
	if in == nil || in.Client == nil {
		return ""
	}
	arts, err := in.Client.ArtifactList()
	if err != nil {
		return ""
	}
	return layerURLFromArtifacts(arts, id)
}

func layerURLFromArtifacts(arts []*ct.Artifact, id string) string {
	var pluginURL string
	for _, a := range arts {
		if a == nil {
			continue
		}
		for _, layer := range layers(a) {
			if layer == nil || layer.ID != id {
				continue
			}
			u := a.LayerURL(layer)
			if u == "" {
				continue
			}
			if a.Meta != nil && a.Meta["flynn.plugin"] == "true" {
				if pluginURL == "" {
					pluginURL = u
				}
				continue
			}
			return u
		}
	}
	return pluginURL
}

func linkOrCopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

package main

import (
	"sort"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
)

// buildArtifactURLError returns why a scoped (non-admin) token may not
// register artifact a, or "" when it may. Only a build:artifacts token reaches
// POST /artifacts without being a cluster admin. Hosts fetch artifact layers
// with the cluster key, so every URL in the artifact must stay inside the
// token's own app prefix (its tarreceive layers included) or the shared
// tarreceive layer store; otherwise a compile could register another app's
// slug and deploy it (NEW-05).
func buildArtifactURLError(tok *authorizer.Token, a *ct.Artifact) string {
	if tok == nil || tok.HasClusterAdmin() {
		return ""
	}
	if a.Type != ct.ArtifactTypeFlynn {
		return "a build may only register flynn image artifacts"
	}
	apps := make([]string, 0, len(tok.AppGrants))
	for _, g := range tok.AppGrants {
		apps = append(apps, g.AppID)
	}
	for _, u := range buildArtifactURLs(a) {
		if !blobstoreauth.BuildArtifactURLAllowed(u, apps) {
			return "artifact URL " + u + " is outside this build's blobstore prefix"
		}
	}
	return ""
}

// buildArtifactURLs is every URL a host may fetch for a: the manifest URI,
// the layer URL template, each manifest layer's URL, and any layer_url.*
// override (whether or not a layer uses it).
func buildArtifactURLs(a *ct.Artifact) []string {
	urls := []string{a.URI}
	if a.LayerURLTemplate != "" {
		urls = append(urls, strings.ReplaceAll(a.LayerURLTemplate, "{id}", "id"))
	}
	if len(a.RawManifest) > 0 {
		for _, rootfs := range a.Manifest().Rootfs {
			if rootfs == nil {
				continue
			}
			for _, layer := range rootfs.Layers {
				if layer != nil {
					urls = append(urls, a.LayerURL(layer))
				}
			}
		}
	}
	keys := make([]string, 0, len(a.Meta))
	for k := range a.Meta {
		if strings.HasPrefix(k, "layer_url.") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		urls = append(urls, strings.TrimSpace(a.Meta[k]))
	}
	return urls
}

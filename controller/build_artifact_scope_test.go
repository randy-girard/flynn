package main

import (
	"encoding/json"
	"net/http"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/buildtoken"
	ct "github.com/randy-girard/flynn/controller/types"
)

// buildArtifactBody is a slug artifact whose manifest names one layer and
// whose URLs come from uri and tmpl, plus any extra meta.
func buildArtifactBody(c *C, uri, tmpl string, meta map[string]string) string {
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Platform: ct.ImagePlatformFor(""),
			Layers: []*ct.ImageLayer{{
				ID:     "abc",
				Type:   ct.ImageLayerTypeSquashfs,
				Length: 1,
				Hashes: map[string]string{"sha512_256": "abc"},
			}},
		}},
	}
	m := map[string]string{"blobstore": "true"}
	for k, v := range meta {
		m[k] = v
	}
	a := &ct.Artifact{
		Type:             ct.ArtifactTypeFlynn,
		URI:              uri,
		Meta:             m,
		RawManifest:      manifest.RawManifest(),
		LayerURLTemplate: tmpl,
	}
	data, err := json.Marshal(a)
	c.Assert(err, IsNil)
	return string(data)
}

// TestBuildTokenArtifactURLsStayInOwnPrefix: hosts fetch artifact layers
// with the cluster key, so an artifact a build token registers must not name
// another app's blobs (NEW-05). The cluster key is not limited.
func (s *S) TestBuildTokenArtifactURLsStayInOwnPrefix(c *C) {
	appA := s.createTestApp(c, &ct.App{Name: "new05-art-a"})
	appB := s.createTestApp(c, &ct.App{Name: "new05-art-b"})
	build, err := buildtoken.Mint(s.tokenSigner, appA, time.Minute)
	c.Assert(err, IsNil)

	base := "http://blobstore.discoverd"
	ownSlug := base + "/slugs/apps/" + appA.ID
	otherSlug := base + "/slugs/apps/" + appB.ID

	// What slugbuilder and dockerbuilder register.
	for _, body := range []string{
		buildArtifactBody(c, ownSlug+"/images/m1.json", ownSlug+"/layers/{id}.squashfs", nil),
		buildArtifactBody(c, base+"/tarreceive/apps/"+appA.ID+"/images/m2.json", base+"/tarreceive/layers/{id}.squashfs", nil),
	} {
		code, out := s.scopedStatus(c, http.MethodPost, build, "/artifacts", body)
		c.Assert(code, Equals, http.StatusOK, Commentf("own artifact refused: %s", out))
	}

	for name, body := range map[string]string{
		"other app image":    buildArtifactBody(c, otherSlug+"/images/m.json", ownSlug+"/layers/{id}.squashfs", nil),
		"other app layers":   buildArtifactBody(c, ownSlug+"/images/m3.json", otherSlug+"/layers/{id}.squashfs", nil),
		"legacy shared slug": buildArtifactBody(c, base+"/slugs/images/m.json", base+"/slugs/layers/{id}.squashfs", nil),
		"layer_url override": buildArtifactBody(c, ownSlug+"/images/m4.json", ownSlug+"/layers/{id}.squashfs", map[string]string{"layer_url.abc": otherSlug + "/layers/abc.squashfs"}),
		"unused override":    buildArtifactBody(c, ownSlug+"/images/m5.json", ownSlug+"/layers/{id}.squashfs", map[string]string{"layer_url.zzz": otherSlug + "/layers/zzz.squashfs"}),
		"host file layer":    buildArtifactBody(c, ownSlug+"/images/m6.json", "file:///var/lib/flynn/{id}.squashfs", nil),
		"repo cache":         buildArtifactBody(c, base+"/repos/"+appB.ID+".tar", ownSlug+"/layers/{id}.squashfs", nil),
		"traversal":          buildArtifactBody(c, ownSlug+"/images/../../"+appB.ID+"/images/m.json", ownSlug+"/layers/{id}.squashfs", nil),
		"external image":     buildArtifactBody(c, "https://example.com/m.json", ownSlug+"/layers/{id}.squashfs", nil),
	} {
		code, out := s.scopedStatus(c, http.MethodPost, build, "/artifacts", body)
		c.Assert(code, Equals, http.StatusForbidden, Commentf("%s: %d %s", name, code, out))
	}

	// A docker-type artifact is not something a compile creates.
	code, _ := s.scopedStatus(c, http.MethodPost, build, "/artifacts", `{"type":"docker","uri":"https://registry.example.com/x"}`)
	c.Assert(code, Equals, http.StatusForbidden)

	// The cluster key keeps the legacy layout (flynn-host, operators).
	legacy := &ct.Artifact{}
	c.Assert(json.Unmarshal([]byte(buildArtifactBody(c, base+"/slugs/images/m8.json", base+"/slugs/layers/{id}.squashfs", nil)), legacy), IsNil)
	c.Assert(s.c.CreateArtifact(legacy), IsNil)
}

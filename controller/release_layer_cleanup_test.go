package main

import (
	"encoding/json"
	"fmt"
	"sort"

	. "github.com/flynn/go-check"
	v1controller "github.com/randy-girard/flynn/controller/client/v1"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

// tarreceiveArtifact is an image whose one layer layerID is stored under
// template (an app's /tarreceive/apps/<app>/layers/ prefix or the shared one).
func (s *S) tarreceiveArtifact(c *C, template, layerID string) *ct.Artifact {
	manifest := ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{Layers: []*ct.ImageLayer{{
			ID:   layerID,
			Type: ct.ImageLayerTypeSquashfs,
			Meta: map[string]string{"tar.layer_id": layerID},
		}}}},
	}
	return s.createTestArtifact(c, &ct.Artifact{
		Type:             ct.ArtifactTypeFlynn,
		URI:              fmt.Sprintf("http://blobstore.discoverd/tarreceive/images/%s.json", random.String(12)),
		Meta:             map[string]string{"blobstore": "true"},
		RawManifest:      manifest.RawManifest(),
		LayerURLTemplate: template,
	})
}

// releaseCleanupFiles is the FileURIs of the release_cleanup job queued for
// releaseID.
func (s *S) releaseCleanupFiles(c *C, releaseID string) []string {
	var raw []byte
	c.Assert(s.hc.db.QueryRow(`SELECT args FROM que_jobs WHERE job_class = 'release_cleanup' AND args->>'ReleaseID' = $1`, releaseID).Scan(&raw), IsNil)
	var args struct{ FileURIs []string }
	c.Assert(json.Unmarshal(raw, &args), IsNil)
	sort.Strings(args.FileURIs)
	return args.FileURIs
}

// EDGE-35: tarreceive stores each app's layers under its own prefix, so two
// apps can hold the same layer id in two different objects. Deleting one
// app's release deletes that app's copy (and its config) even though the
// other app still has an artifact naming the same id; a layer two artifacts
// share at the same URL is still kept.
func (s *S) TestDeleteReleaseDeletesAppLayerWithIDSharedByAnotherApp(c *C) {
	layerID := random.Hex(32)
	appA := s.createTestApp(c, &ct.App{Name: "edge35-a-" + random.String(6)})
	appB := s.createTestApp(c, &ct.App{Name: "edge35-b-" + random.String(6)})
	tmplA := "http://blobstore.discoverd/tarreceive/apps/" + appA.ID + "/layers/{id}.squashfs"
	tmplB := "http://blobstore.discoverd/tarreceive/apps/" + appB.ID + "/layers/{id}.squashfs"
	artA := s.tarreceiveArtifact(c, tmplA, layerID)
	artB := s.tarreceiveArtifact(c, tmplB, layerID)
	relA := s.createTestRelease(c, appA.ID, &ct.Release{ArtifactIDs: []string{artA.ID}})
	s.createTestRelease(c, appB.ID, &ct.Release{ArtifactIDs: []string{artB.ID}})

	hc := s.c.(*v1controller.Client)
	c.Assert(hc.Delete(fmt.Sprintf("/apps/%s/releases/%s", appA.ID, relA.ID), nil), IsNil)
	prefix := "http://blobstore.discoverd/tarreceive/apps/" + appA.ID + "/layers/" + layerID
	want := []string{artA.URI, prefix + ".json", prefix + ".squashfs"}
	sort.Strings(want)
	c.Assert(s.releaseCleanupFiles(c, relA.ID), DeepEquals, want)

	// the same layer URL in two artifacts: the first deletion keeps it
	shared := "http://blobstore.discoverd/tarreceive/layers/{id}.squashfs"
	layer2 := random.Hex(32)
	art1 := s.tarreceiveArtifact(c, shared, layer2)
	art2 := s.tarreceiveArtifact(c, shared, layer2)
	rel1 := s.createTestRelease(c, appA.ID, &ct.Release{ArtifactIDs: []string{art1.ID}})
	s.createTestRelease(c, appB.ID, &ct.Release{ArtifactIDs: []string{art2.ID}})
	c.Assert(hc.Delete(fmt.Sprintf("/apps/%s/releases/%s", appA.ID, rel1.ID), nil), IsNil)
	c.Assert(s.releaseCleanupFiles(c, rel1.ID), DeepEquals, []string{art1.URI})
}

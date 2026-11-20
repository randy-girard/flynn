package dockerimage

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
)

// NEW-05: a dockerbuilder image manifest goes under the app's own prefix;
// its layers stay in the shared tarreceive store that only tarreceive writes.
func TestImageArtifactUsesAppPrefix(t *testing.T) {
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{ID: "abc", Type: ct.ImageLayerTypeSquashfs}},
		}},
	}
	a := imageArtifact(&BuildResult{Manifest: manifest}, "img-1", map[string]string{"x": "y"}, "app-1")
	if want := "http://blobstore.discoverd/tarreceive/apps/app-1/images/" + manifest.ID() + ".json"; a.URI != want {
		t.Fatalf("URI = %s, want %s", a.URI, want)
	}
	apps := []string{"app-1"}
	if !blobstoreauth.BuildArtifactURLAllowed(a.URI, apps) || !blobstoreauth.BuildArtifactURLAllowed(a.LayerURL(manifest.Rootfs[0].Layers[0]), apps) {
		t.Fatalf("image artifact URLs must be inside app-1's build prefix: %s %s", a.URI, a.LayerURL(manifest.Rootfs[0].Layers[0]))
	}
	if a.Meta["blobstore"] != "true" || a.Meta["x"] != "y" || a.ID != "img-1" {
		t.Fatalf("artifact = %+v", a)
	}
	// No app (an operator import with the cluster key): the legacy path.
	if a := imageArtifact(&BuildResult{Manifest: manifest}, "", nil, ""); a.URI != "http://blobstore.discoverd/tarreceive/images/"+manifest.ID()+".json" {
		t.Fatalf("legacy URI = %s", a.URI)
	}
}

func TestUploadManifestTreatsExistingObjectAsDone(t *testing.T) {
	for _, c := range []struct {
		code int
		ok   bool
	}{
		{http.StatusOK, true},
		{http.StatusConflict, true},
		{http.StatusUnauthorized, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
		}))
		err := uploadManifest([]byte("{}"), srv.URL+"/tarreceive/apps/app-1/images/x.json")
		srv.Close()
		if (err == nil) != c.ok {
			t.Errorf("status %d: err = %v", c.code, err)
		}
	}
}

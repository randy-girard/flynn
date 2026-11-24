package dockerimage

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	tarclient "github.com/randy-girard/flynn/tarreceive/client"
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

// EDGE-35: tarreceive stores a pushing app's layers under that app's prefix,
// so the artifact's layer template names the same prefix. No app keeps the
// legacy shared store.
func TestImageArtifactUsesAppLayerTemplate(t *testing.T) {
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{ID: "abc", Type: ct.ImageLayerTypeSquashfs}},
		}},
	}
	a := imageArtifact(&BuildResult{Manifest: manifest}, "img-1", nil, "app-1")
	if want := "http://blobstore.discoverd/tarreceive/apps/app-1/layers/{id}.squashfs"; a.LayerURLTemplate != want {
		t.Fatalf("template = %s, want %s", a.LayerURLTemplate, want)
	}
	if a := imageArtifact(&BuildResult{Manifest: manifest}, "", nil, ""); a.LayerURLTemplate != "http://blobstore.discoverd/tarreceive/layers/{id}.squashfs" {
		t.Fatalf("legacy template = %s", a.LayerURLTemplate)
	}
}

// The tarreceive client used for the layers names the same app as the
// artifact, so both land under one prefix.
func TestImportSaveDirSendsAppToTarreceive(t *testing.T) {
	var apps []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apps = append(apps, r.URL.Query().Get("app"))
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tc := tarclient.NewClient(srv.URL, "key")
	opts := ImportOptions{TarClient: tc, AppID: "app-1"}
	if got := opts.tarClient(); got.App != "app-1" || tc.App != "" {
		t.Fatalf("tar client app = %q (caller's %q)", got.App, tc.App)
	}
	if _, err := opts.tarClient().GetLayer("abc"); err == nil {
		t.Fatal("expected not found")
	}
	if len(apps) != 1 || apps[0] != "app-1" {
		t.Fatalf("app sent = %v", apps)
	}
}

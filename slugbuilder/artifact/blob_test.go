package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
)

// NEW-05: the slug and its manifest go under the app's own prefix, so the
// artifact the build token registers stays inside what that token may name.
func TestSlugArtifactUsesAppPrefix(t *testing.T) {
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{ID: "abc", Type: ct.ImageLayerTypeSquashfs}},
		}},
	}
	a := slugArtifact("app-1", "img-1", manifest)
	wantURI := "http://blobstore.discoverd/slugs/apps/app-1/images/" + manifest.ID() + ".json"
	if a.URI != wantURI {
		t.Fatalf("URI = %s, want %s", a.URI, wantURI)
	}
	if got := a.LayerURL(manifest.Rootfs[0].Layers[0]); got != blobstoreauth.SlugLayerURL("app-1", "abc") {
		t.Fatalf("layer URL = %s", got)
	}
	apps := []string{"app-1"}
	if !blobstoreauth.BuildArtifactURLAllowed(a.URI, apps) || !blobstoreauth.BuildArtifactURLAllowed(a.LayerURL(manifest.Rootfs[0].Layers[0]), apps) {
		t.Fatal("slug artifact URLs must be inside app-1's build prefix")
	}
	if a.ID != "img-1" || a.Meta["blobstore"] != "true" {
		t.Fatalf("artifact = %+v", a)
	}
}

// NEW-05: the blobstore answers 409 when a content-addressed object is
// already stored; the bytes under that name are the same slug, so the
// upload is done.
func TestUploadTreatsExistingObjectAsDone(t *testing.T) {
	for _, c := range []struct {
		code int
		ok   bool
	}{
		{http.StatusOK, true},
		{http.StatusConflict, true},
		{http.StatusForbidden, false},
		{http.StatusInternalServerError, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
		}))
		err := upload(strings.NewReader("x"), srv.URL+"/slugs/apps/app-1/layers/abc.squashfs")
		srv.Close()
		if (err == nil) != c.ok {
			t.Errorf("status %d: err = %v", c.code, err)
		}
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/tarreceive/utils"
)

// EDGE-35: a layer is stored under the pushing app's blobstore prefix, so it
// counts toward that app's APP_QUOTA. The shared /tarreceive/layers store is
// left to cluster-key callers that name no app (old CLIs, system imports).

// recordedUploads collects the blobstore PUTs a fake upload saw.
type recordedUploads struct {
	mu   sync.Mutex
	urls []string
	body map[string][]byte
}

func (u *recordedUploads) upload(r io.Reader, url string) error {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.urls = append(u.urls, url)
	if u.body == nil {
		u.body = map[string][]byte{}
	}
	u.body[url] = data
	return nil
}

func (u *recordedUploads) list() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.urls...)
}

func fakeSquash(src, dst string) error { return ioutil.WriteFile(dst, []byte("squashfs"), 0644) }

func appLayerURL(app, id string) string {
	return "http://blobstore.discoverd/tarreceive/apps/" + app + "/layers/" + id + ".squashfs"
}

func appConfigURL(app, id string) string {
	return "http://blobstore.discoverd/tarreceive/apps/" + app + "/layers/" + id + ".json"
}

func postLayer(s *server, path string, body []byte, auth func(*http.Request)) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	auth(req)
	s.ServeHTTP(rec, req)
	return rec
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func clusterKey(r *http.Request) { r.SetBasicAuth("", "tar-secret") }

func TestCreateLayerStoresUnderAppPrefix(t *testing.T) {
	body, id := layerTar(t, "edge-35")
	for _, c := range []struct {
		name string
		path string
		auth func(*http.Request)
		code int
		want []string
	}{
		// a build token's layer goes under its own app, named or not
		{"build token, no app", "/layer/" + id, nil, 200, []string{appLayerURL("app-1", id), appConfigURL("app-1", id)}},
		{"build token, own app", "/layer/" + id + "?app=app-1", nil, 200, []string{appLayerURL("app-1", id), appConfigURL("app-1", id)}},
		{"build token, other app", "/layer/" + id + "?app=app-2", nil, 403, nil},
		{"build token, bad app", "/layer/" + id + "?app=../layers", nil, 400, nil},
		// the cluster key may name any app; without one it keeps the
		// legacy shared store
		{"cluster key, app", "/layer/" + id + "?app=app-9", clusterKey, 200, []string{appLayerURL("app-9", id), appConfigURL("app-9", id)}},
		{"cluster key, no app", "/layer/" + id, clusterKey, 200, []string{utils.LayerURL(id), utils.ConfigURL(id)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, tok := buildServer(t)
			s.squash = fakeSquash
			var up recordedUploads
			s.upload = up.upload
			auth := c.auth
			if auth == nil {
				auth = bearer(tok)
			}
			rec := postLayer(s, c.path, body, auth)
			if rec.Code != c.code {
				t.Fatalf("POST %s = %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
			}
			if got := up.list(); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("uploads = %v, want %v", got, c.want)
			}
		})
	}
}

// A build token with grants for several apps has to say which one.
func TestCreateLayerMultiAppTokenMustNameApp(t *testing.T) {
	s, tok := buildServerFor(t, "app-1", "app-2")
	s.squash = fakeSquash
	var up recordedUploads
	s.upload = up.upload
	body, id := layerTar(t, "two apps")
	if rec := postLayer(s, "/layer/"+id, body, bearer(tok)); rec.Code != http.StatusBadRequest {
		t.Fatalf("no app = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := postLayer(s, "/layer/"+id+"?app=app-2", body, bearer(tok)); rec.Code != http.StatusOK {
		t.Fatalf("app-2 = %d %s", rec.Code, rec.Body.String())
	}
	if got := up.list(); len(got) != 2 || got[0] != appLayerURL("app-2", id) {
		t.Fatalf("uploads = %v", got)
	}
}

// TestCreateLayerCountsAgainstAppQuota: the blobstore refuses the layer
// because it counts toward the app's APP_QUOTA (EDGE-08 limits.go); the push
// gets that 507 and the message naming the setting, not a 500.
func TestCreateLayerCountsAgainstAppQuota(t *testing.T) {
	s, tok := buildServer(t)
	s.squash = fakeSquash
	var urls []string
	const msg = "app app-1 already stores 900 bytes and the blobstore quota per app is 1 KiB; delete old releases or caches, or an operator can raise it with `flynn -a blobstore env:set APP_QUOTA=<size>`"
	s.upload = func(r io.Reader, url string) error {
		io.Copy(ioutil.Discard, r)
		urls = append(urls, url)
		return &blobstoreError{Status: http.StatusInsufficientStorage, Message: msg}
	}
	body, id := layerTar(t, "over quota")
	rec := postLayer(s, "/layer/"+id, body, bearer(tok))
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("over-quota layer = %d %s, want 507", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "APP_QUOTA") {
		t.Fatalf("error does not name APP_QUOTA: %s", rec.Body.String())
	}
	if len(urls) != 1 || urls[0] != appLayerURL("app-1", id) {
		t.Fatalf("uploads = %v, want only the app's layer (no config after a refusal)", urls)
	}
}

// A layer another app already stored is not reused: this app stores (and is
// charged for) its own copy, so the quota check runs for it.
func TestExistingLayerIsReusedOnlyWithinTheApp(t *testing.T) {
	s, tok := buildServer(t)
	s.squash = fakeSquash
	_, id := layerTar(t, "shared")
	stored := &ct.ImageLayer{ID: id, Type: ct.ImageLayerTypeSquashfs, Length: 8, Hashes: map[string]string{"sha512_256": "x"}}
	var asked []string
	s.fetchLayer = func(app, got string) (*ct.ImageLayer, error) {
		asked = append(asked, app+"/"+got)
		if app == "app-other" && got == id {
			return stored, nil
		}
		return nil, ct.ErrNotFound
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/layer/"+id, nil)
	bearer(tok)(req)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET another app's layer = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if len(asked) != 1 || asked[0] != "app-1/"+id {
		t.Fatalf("lookups = %v, want app-1 only", asked)
	}
}

// Reusing a stored layer refreshes its config (so the layer GC's grace period
// starts again) and checks the squashfs is still there; a layer the GC
// removed in the meantime is reported missing so the client uploads it again.
func TestReusedLayerIsTouchedAndChecked(t *testing.T) {
	s, tok := buildServer(t)
	s.squash = fakeSquash
	body, id := layerTar(t, "reuse")
	stored := &ct.ImageLayer{ID: id, Type: ct.ImageLayerTypeSquashfs, Length: 8, Hashes: map[string]string{"sha512_256": "x"}}
	s.fetchLayer = func(app, got string) (*ct.ImageLayer, error) { return stored, nil }
	var up recordedUploads
	s.upload = up.upload
	present := true
	var checked []string
	s.layerExists = func(url string) (bool, error) {
		checked = append(checked, url)
		return present, nil
	}

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/layer/"+id, nil)
		bearer(tok)(req)
		s.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(); rec.Code != http.StatusOK {
		t.Fatalf("GET stored layer = %d %s", rec.Code, rec.Body.String())
	}
	if got := up.list(); len(got) != 1 || got[0] != appConfigURL("app-1", id) {
		t.Fatalf("touch uploads = %v, want the config re-put", got)
	}
	var touched ct.ImageLayer
	if err := json.Unmarshal(up.body[appConfigURL("app-1", id)], &touched); err != nil || touched.Hashes["sha512_256"] != "x" {
		t.Fatalf("touched config = %s (%v), want the stored one", up.body[appConfigURL("app-1", id)], err)
	}
	if len(checked) != 1 || checked[0] != appLayerURL("app-1", id) {
		t.Fatalf("checked = %v", checked)
	}

	// the squashfs is gone: GET says missing and POST rebuilds it
	present = false
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Fatalf("GET layer with a missing squashfs = %d, want 404", rec.Code)
	}
	up = recordedUploads{}
	s.upload = up.upload
	if rec := postLayer(s, "/layer/"+id, body, bearer(tok)); rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	got := up.list()
	if len(got) != 3 || got[1] != appLayerURL("app-1", id) || got[2] != appConfigURL("app-1", id) {
		t.Fatalf("uploads = %v, want touch, squashfs, config", got)
	}
}

func TestReusedLayerCheckErrorFails(t *testing.T) {
	s, tok := buildServer(t)
	_, id := layerTar(t, "reuse")
	s.fetchLayer = func(app, got string) (*ct.ImageLayer, error) {
		return &ct.ImageLayer{ID: id}, nil
	}
	s.layerExists = func(url string) (bool, error) { return false, errors.New("blobstore down") }
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/layer/"+id, nil)
	bearer(tok)(req)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET with a failing check = %d, want 500", rec.Code)
	}
}

// The manifest and the layer template of an artifact follow the same app as
// its layers.
func TestCreateArtifactUsesAppPrefix(t *testing.T) {
	s, tok := buildServer(t)
	var up recordedUploads
	s.upload = up.upload
	var created *ct.Artifact
	s.createArtifact = func(a *ct.Artifact) error { created = a; return nil }
	manifest := ct.ImageManifest{Type: ct.ImageManifestTypeV1, Rootfs: []*ct.ImageRootfs{{Layers: []*ct.ImageLayer{{ID: "abc", Type: ct.ImageLayerTypeSquashfs}}}}}
	data := manifest.RawManifest()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/artifact", bytes.NewReader(data))
	bearer(tok)(req)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /artifact = %d %s", rec.Code, rec.Body.String())
	}
	wantURI := "http://blobstore.discoverd/tarreceive/apps/app-1/images/" + manifest.ID() + ".json"
	if created == nil || created.URI != wantURI {
		t.Fatalf("artifact = %+v, want URI %s", created, wantURI)
	}
	if created.LayerURLTemplate != "http://blobstore.discoverd/tarreceive/apps/app-1/layers/{id}.squashfs" {
		t.Fatalf("template = %s", created.LayerURLTemplate)
	}
	if got := created.LayerURL(manifest.Rootfs[0].Layers[0]); got != appLayerURL("app-1", "abc") {
		t.Fatalf("layer URL = %s", got)
	}
	if got := up.list(); len(got) != 1 || got[0] != wantURI {
		t.Fatalf("uploads = %v", got)
	}

	// the cluster key with no app keeps the legacy shared paths
	created = nil
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/artifact", bytes.NewReader(data))
	clusterKey(req)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || created == nil || created.LayerURLTemplate != utils.LayerURLTemplate {
		t.Fatalf("legacy artifact = %d %+v", rec.Code, created)
	}
}

func TestUtilsAppURLs(t *testing.T) {
	if got := utils.LayerURLFor("app-1", "abc"); got != appLayerURL("app-1", "abc") {
		t.Fatal(got)
	}
	if got := utils.ConfigURLFor("app-1", "abc"); got != appConfigURL("app-1", "abc") {
		t.Fatal(got)
	}
	if utils.LayerURLFor("", "abc") != utils.LayerURL("abc") || utils.ConfigURLFor("", "abc") != utils.ConfigURL("abc") {
		t.Fatal("empty app is not the shared store")
	}
	if got := utils.LayerURLTemplateFor(""); got != utils.LayerURLTemplate {
		t.Fatal(got)
	}
	if got := utils.ConfigURLForLayerURL(appLayerURL("a", "x")); got != appConfigURL("a", "x") {
		t.Fatal(got)
	}
}

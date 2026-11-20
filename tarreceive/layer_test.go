package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// NEW-05: a layer is named by the SHA256 of its tar. Once its config is
// stored, a second POST must not rebuild and replace the squashfs: the new
// bytes (and so the sha512_256 in the config) can differ, which breaks every
// image that already names the old hash.
func TestCreateLayerKeepsExistingLayer(t *testing.T) {
	pubKey, privKey, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := authorizer.ParseTokenKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	sign, err := tokensigner.ParseSigningKey(privKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	build, err := sign.Sign(&api.AccessToken{
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(10 * time.Minute)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants:  []*api.AppGrant{{AppId: "app-1", Permissions: []string{"app:write"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(authorizer.New([]string{"tar-secret"}, nil, pk, time.Hour), nil)

	const id = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	existing := &ct.ImageLayer{ID: id, Type: ct.ImageLayerTypeSquashfs, Length: 42, Hashes: map[string]string{"sha512_256": "old"}}
	var lookups int
	s.fetchLayer = func(got string) (*ct.ImageLayer, error) {
		lookups++
		if got != id {
			return nil, ct.ErrNotFound
		}
		return existing, nil
	}

	post := func(layerID, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/layer/"+layerID, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+build)
		s.ServeHTTP(rec, req)
		return rec
	}

	// The body is not the tar for id; the stored layer comes back untouched.
	rec := post(id, "not the original tar")
	if rec.Code != http.StatusOK {
		t.Fatalf("existing layer POST = %d %s", rec.Code, rec.Body.String())
	}
	var got ct.ImageLayer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Hashes["sha512_256"] != "old" || got.Length != 42 {
		t.Fatalf("layer = %+v, want the stored one", got)
	}

	// A new id still goes through the upload path (which then rejects the
	// mismatched body).
	if rec := post("00"+id[2:], "x"); rec.Code == http.StatusOK {
		t.Fatalf("new layer with a bad body = %d", rec.Code)
	}
	if lookups != 2 {
		t.Fatalf("lookups = %d, want 2", lookups)
	}
}

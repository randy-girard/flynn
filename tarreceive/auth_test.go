package main

import (
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/tokensigner"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/tarreceive/utils"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTarreceiveAuth(t *testing.T) {
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
	auth := authorizer.New([]string{"tar-secret"}, nil, pk, time.Hour)
	s := newServer(auth, nil)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", status.Path, nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/artifact", nil))
	if rec.Code != 401 {
		t.Fatalf("unauthed=%d", rec.Code)
	}

	now := time.Now()
	appTok, err := sign.Sign(&api.AccessToken{
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(10 * time.Minute)),
		AppGrants:  []*api.AppGrant{{AppId: "app-1", Permissions: []string{"app:write"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/artifact", nil)
	req.Header.Set("Authorization", "Bearer "+appTok)
	s.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("app-scoped token must not push layers, status=%d", rec.Code)
	}
}

func TestTarreceiveURLs(t *testing.T) {
	if utils.ConfigURL("abc") != "http://blobstore.discoverd/tarreceive/layers/abc.json" {
		t.Fatal(utils.ConfigURL("abc"))
	}
	if utils.LayerURL("abc") != "http://blobstore.discoverd/tarreceive/layers/abc.squashfs" {
		t.Fatal(utils.LayerURL("abc"))
	}
}

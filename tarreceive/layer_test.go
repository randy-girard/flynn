package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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

// buildServer returns a tarreceive server and a build:artifacts token for it.
func buildServer(t *testing.T) (*server, string) {
	t.Helper()
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
	tok, err := sign.Sign(&api.AccessToken{
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(10 * time.Minute)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants:  []*api.AppGrant{{AppId: "app-1", Permissions: []string{"app:write"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(authorizer.New([]string{"tar-secret"}, nil, pk, time.Hour), nil)
	s.fetchLayer = func(string) (*ct.ImageLayer, error) { return nil, ct.ErrNotFound }
	s.upload = func(r io.Reader, url string) error {
		_, err := io.Copy(ioutil.Discard, r)
		return err
	}
	return s, tok
}

// layerTar is a one-file tar and its SHA256 (the layer id).
func layerTar(t *testing.T, content string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0644, Size: int64(len(content)), Uid: os.Getuid(), Gid: os.Getgid(), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// onlyReader hides the length of a body so the request is sent chunked.
type onlyReader struct{ io.Reader }

// EDGE-08: a layer larger than MAX_LAYER_SIZE is refused with 413 before
// anything is written to disk when the length is known, and as soon as the
// limit is passed when it is not.
func TestCreateLayerRejectsOversizeBody(t *testing.T) {
	s, tok := buildServer(t)
	s.maxLayerSize = 4096
	var squashed int32
	s.squash = func(src, dst string) error {
		atomic.AddInt32(&squashed, 1)
		return ioutil.WriteFile(dst, []byte("squashfs"), 0644)
	}
	var lookups int32
	s.fetchLayer = func(string) (*ct.ImageLayer, error) {
		atomic.AddInt32(&lookups, 1)
		return nil, ct.ErrNotFound
	}

	big, id := layerTar(t, strings.Repeat("x", 8192))
	for _, c := range []struct {
		name string
		body io.Reader
	}{
		{"content-length", bytes.NewReader(big)},
		{"chunked", onlyReader{bytes.NewReader(big)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/layer/"+id, c.body)
			if _, ok := c.body.(onlyReader); ok {
				req.ContentLength = -1
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversize layer = %d %s, want 413", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "MAX_LAYER_SIZE") {
				t.Fatalf("error does not name the setting: %s", rec.Body.String())
			}
		})
	}
	if n := atomic.LoadInt32(&squashed); n != 0 {
		t.Fatalf("oversize layers reached mksquashfs %d times", n)
	}
	if n := atomic.LoadInt32(&lookups); n != 1 {
		// the known-length request is refused before the lookup
		t.Fatalf("layer lookups = %d, want 1 (chunked only)", n)
	}

	// a layer under the limit still goes through
	small, smallID := layerTar(t, "ok")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/layer/"+smallID, bytes.NewReader(small))
	req.Header.Set("Authorization", "Bearer "+tok)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("small layer = %d %s", rec.Code, rec.Body.String())
	}
}

// EDGE-08: at most MAX_CONCURRENT_LAYERS uploads unpack and squash at once;
// the rest wait for a slot instead of failing.
func TestCreateLayerLimitsConcurrency(t *testing.T) {
	s, tok := buildServer(t)
	s.sem = make(chan struct{}, 2)
	var cur, peak int32
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	s.squash = func(src, dst string) error {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		atomic.AddInt32(&cur, -1)
		return ioutil.WriteFile(dst, []byte("squashfs"), 0644)
	}

	const total = 4
	codes := make(chan int, total)
	for i := 0; i < total; i++ {
		body, id := layerTar(t, fmt.Sprintf("layer %d", i))
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/layer/"+id, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			s.ServeHTTP(rec, req)
			codes <- rec.Code
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("uploads never reached mksquashfs")
		}
	}
	select {
	case <-entered:
		t.Fatal("a third upload squashed while two were running")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for i := 0; i < total; i++ {
		if code := <-codes; code != http.StatusOK {
			t.Fatalf("upload %d = %d", i, code)
		}
	}
	if p := atomic.LoadInt32(&peak); p != 2 {
		t.Fatalf("peak concurrent squashes = %d, want 2", p)
	}
}

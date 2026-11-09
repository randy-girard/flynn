package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestBlobstoreRawHTTPCommandUsesNC(t *testing.T) {
	cmd := blobstoreRawHTTPCommand("GET", "http://blobstore.discoverd/layers/abc")
	if len(cmd) != 6 || cmd[0] != "sh" || cmd[1] != "-c" || cmd[4] != "GET" {
		t.Fatalf("command = %#v", cmd)
	}
	if cmd[5] != "http://blobstore.discoverd/layers/abc" {
		t.Fatalf("url arg = %q", cmd[5])
	}
	if strings.Contains(cmd[2], "curl") {
		t.Fatal("blobstore job must not exec curl")
	}
	if !strings.Contains(cmd[2], "nc ") {
		t.Fatal("script must speak HTTP with nc")
	}
	if !strings.Contains(cmd[2], "Authorization: Bearer") {
		t.Fatal("script must send the blobstore key")
	}
	if strings.Contains(cmd[2], "http://blobstore.discoverd/layers/abc") {
		t.Fatal("url must be an argument, not part of the script")
	}
	if !strings.Contains(cmd[2], "blobstore GET produced no response") {
		t.Fatal("GET must fail when nc prints no HTTP response")
	}
}

func TestBlobstoreHTTPRunConfigUsesSystemApp(t *testing.T) {
	release := &ct.Release{ID: "rel", ArtifactIDs: []string{"art"}}
	cfg := blobstoreHTTPRunConfig(release, blobstoreRawHTTPCommand("PUT", "http://blobstore.discoverd/x"), nil, nil, strings.NewReader("layer"))
	if cfg.App != "blobstore" || !cfg.ReleaseEnv || cfg.Release != "rel" {
		t.Fatalf("config = %+v", cfg)
	}
	if len(cfg.Artifacts) != 1 || cfg.Artifacts[0] != "art" || cfg.DeprecatedArtifact != "art" {
		t.Fatalf("artifacts = %#v", cfg.Artifacts)
	}
}

func TestBlobstoreRawHTTPCommandGETFailsWithoutResponse(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc is not installed")
	}
	cmd := blobstoreRawHTTPCommand("GET", "http://127.0.0.1:1/layers/abc")
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err == nil {
		t.Fatalf("GET with no HTTP response exited 0: %s", out)
	}
}

func TestBlobstoreRawHTTPCommandRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc is not installed")
	}
	var gotAuth, gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if r.Method == http.MethodPut && gotAuth != "Bearer secret-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("blob"))
	}))
	defer srv.Close()

	t.Setenv("AUTH_KEY", "secret-key")
	t.Setenv("CONTROLLER_KEY", "other-key")

	get := blobstoreRawHTTPCommand("GET", srv.URL+"/layers/abc")
	out, err := exec.Command(get[0], get[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("GET: %v\n%s", err, out)
	}
	if gotAuth != "Bearer secret-key" || gotMethod != http.MethodGet || gotPath != "/layers/abc" {
		t.Fatalf("request auth=%q method=%s path=%s", gotAuth, gotMethod, gotPath)
	}
	if !bytes.Contains(out, []byte("200")) || !bytes.Contains(out, []byte("blob")) {
		t.Fatalf("GET response = %q", out)
	}

	put := blobstoreRawHTTPCommand("PUT", srv.URL+"/layers/abc")
	cmd := exec.Command(put[0], put[1:]...)
	cmd.Stdin = strings.NewReader("hello-layer")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PUT: %v\n%s", err, out)
	}
	if gotMethod != http.MethodPut || gotBody != "hello-layer" || gotAuth != "Bearer secret-key" {
		t.Fatalf("PUT method=%s auth=%q body=%q", gotMethod, gotAuth, gotBody)
	}

	t.Setenv("AUTH_KEY", "")
	t.Setenv("CONTROLLER_KEY", "")
	denied := blobstoreRawHTTPCommand("PUT", srv.URL+"/layers/abc")
	bad := exec.Command(denied[0], denied[1:]...)
	bad.Stdin = strings.NewReader("nope")
	if err := bad.Run(); err == nil {
		t.Fatal("PUT without a key must fail")
	}
}

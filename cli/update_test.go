package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCLIAssetName(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "flynn-linux-amd64.gz"},
		{"linux", "arm64", "flynn-linux-arm64.gz"},
		{"darwin", "amd64", "flynn-darwin-amd64.gz"},
		{"darwin", "arm64", "flynn-darwin-arm64.gz"},
		{"windows", "amd64", "flynn-windows-amd64.exe.gz"},
	}
	for _, tc := range cases {
		if got := cliAssetName(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("cliAssetName(%s,%s)=%s want %s", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestCLINeedsUpdate(t *testing.T) {
	if cliNeedsUpdate("v20260915.0", "v20260915.0") {
		t.Fatal("same tag")
	}
	if cliNeedsUpdate("v1", "") {
		t.Fatal("empty latest")
	}
	if !cliNeedsUpdate("dev", "v20260915.0") {
		t.Fatal("dev should update")
	}
	if !cliNeedsUpdate("v20260914.0", "v20260915.0") {
		t.Fatal("older should update")
	}
}

func TestParseAndVerifySHA512(t *testing.T) {
	payload := []byte("gz-bytes")
	hexSum := sha512Hex(payload)
	sums := parseSHA512Sums([]byte(fmt.Sprintf("%s  flynn-linux-amd64.gz\n%s *./flynn-darwin-arm64.gz\n# comment\nbadline\n", hexSum, hexSum)))
	if sums["flynn-linux-amd64.gz"] != hexSum || sums["flynn-darwin-arm64.gz"] != hexSum {
		t.Fatalf("%v", sums)
	}
	if err := verifySHA512(payload, hexSum); err != nil {
		t.Fatal(err)
	}
	if err := verifySHA512(payload, strings.ToUpper(hexSum)); err != nil {
		t.Fatal(err)
	}
	if err := verifySHA512(payload, "deadbeef"); err == nil {
		t.Fatal("mismatch")
	}
}

func TestUpdaterLatestTag(t *testing.T) {
	t.Setenv("FLYNN_GITHUB_TOKEN", "")
	t.Setenv("FLYNN_PLUGIN_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("FLYNN_GITHUB_REPO", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/randy-girard/flynn/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "flynn-cli" {
			t.Errorf("User-Agent=%q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization=%q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"tag_name":"v20260915.0"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := &Updater{HTTP: srv.Client(), API: srv.URL, Repo: "randy-girard/flynn"}
	tag, err := u.latestTag()
	if err != nil || tag != "v20260915.0" {
		t.Fatalf("%q %v", tag, err)
	}
}

func TestUpdaterCheckAvailable(t *testing.T) {
	t.Setenv("FLYNN_VERSION", "v20200101.0")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/flynn/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v20260915.0"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := &Updater{HTTP: srv.Client(), API: srv.URL, Repo: "acme/flynn"}
	if err := u.run(updateOptions{Check: true}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdaterAlreadyUpToDateAndApply(t *testing.T) {
	t.Setenv("FLYNN_GITHUB_TOKEN", "")
	t.Setenv("FLYNN_PLUGIN_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("FLYNN_GITHUB_REPO", "")

	gz := gzipBytes(t, []byte("fake-cli"))
	hexSum := sha512Hex(gz)
	asset := cliAssetName(runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/flynn/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization=%q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"tag_name":"v20260915.0"}`))
	})
	mux.HandleFunc("/acme/flynn/releases/download/v20260915.0/checksums.sha512", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hexSum, asset)
	})
	mux.HandleFunc("/acme/flynn/releases/download/v20260915.0/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(gz)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var got []byte
	u := &Updater{
		HTTP: srv.Client(),
		Repo: "acme/flynn",
		API:  srv.URL,
		DL:   srv.URL,
		Apply: func(r io.Reader) error {
			b, err := io.ReadAll(r)
			got = b
			return err
		},
	}

	t.Setenv("FLYNN_VERSION", "v20260915.0")
	if err := u.run(updateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("already current must not download")
	}

	t.Setenv("FLYNN_VERSION", "v20200101.0")
	if err := u.run(updateOptions{Version: "v20260915.0"}); err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-cli" {
		t.Fatalf("applied %q", got)
	}
}

func TestUpdaterLatestTagErrors(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":""}`))
	}))
	t.Cleanup(empty.Close)
	u := &Updater{HTTP: empty.Client(), API: empty.URL, Repo: "acme/flynn"}
	if _, err := u.latestTag(); err == nil {
		t.Fatal("empty tag")
	}

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(notFound.Close)
	u.HTTP, u.API = notFound.Client(), notFound.URL
	if _, err := u.latestTag(); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("404: %v", err)
	}
}

func TestUpdaterChecksumMismatch(t *testing.T) {
	t.Setenv("FLYNN_VERSION", "v1")
	asset := cliAssetName(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/acme/flynn/releases/download/v2/checksums.sha512", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "deadbeef  %s\n", asset)
	})
	mux.HandleFunc("/acme/flynn/releases/download/v2/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("nope"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u := &Updater{HTTP: srv.Client(), Repo: "acme/flynn", DL: srv.URL, Apply: func(io.Reader) error { return nil }}
	if err := u.run(updateOptions{Version: "v2"}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
}

func TestUpdaterMissingChecksumEntry(t *testing.T) {
	t.Setenv("FLYNN_VERSION", "v1")
	asset := cliAssetName(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/acme/flynn/releases/download/v2/checksums.sha512", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "abcd  flynn-other.gz")
	})
	mux.HandleFunc("/acme/flynn/releases/download/v2/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("nope"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u := &Updater{HTTP: srv.Client(), Repo: "acme/flynn", DL: srv.URL, Apply: func(io.Reader) error { return nil }}
	if err := u.run(updateOptions{Version: "v2"}); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("got %v", err)
	}
}

func TestReadWriteUpdateTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cktime")
	if !readTime(path).IsZero() {
		t.Fatal("missing file")
	}
	want := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if !writeTime(path, want) {
		t.Fatal("write")
	}
	got := readTime(path)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGithubUpdateTokenOrder(t *testing.T) {
	t.Setenv("FLYNN_GITHUB_TOKEN", "a")
	t.Setenv("FLYNN_PLUGIN_GITHUB_TOKEN", "b")
	t.Setenv("GITHUB_TOKEN", "c")
	if githubUpdateToken() != "a" {
		t.Fatal(githubUpdateToken())
	}
	t.Setenv("FLYNN_GITHUB_TOKEN", "")
	if githubUpdateToken() != "b" {
		t.Fatal(githubUpdateToken())
	}
	t.Setenv("FLYNN_PLUGIN_GITHUB_TOKEN", "")
	if githubUpdateToken() != "c" {
		t.Fatal(githubUpdateToken())
	}
	t.Setenv("GITHUB_TOKEN", "")
	if githubUpdateToken() != "" {
		t.Fatal(githubUpdateToken())
	}
}

func TestUpdaterRepoFromEnv(t *testing.T) {
	t.Setenv("FLYNN_GITHUB_REPO", "env/flynn")
	u := &Updater{}
	if u.repo() != "env/flynn" {
		t.Fatal(u.repo())
	}
	u.Repo = "explicit/flynn"
	if u.repo() != "explicit/flynn" {
		t.Fatal(u.repo())
	}
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func sha512Hex(payload []byte) string {
	sum := sha512.Sum512(payload)
	return hex.EncodeToString(sum[:])
}

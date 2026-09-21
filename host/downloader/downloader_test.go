package downloader

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
)

func TestVerifySHA512FileMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.gz")
	payload := []byte("sec-032-payload")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(payload)
	if err := verifySHA512File(path, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	err := verifySHA512File(path, "deadbeef")
	if err == nil {
		t.Fatal("expected checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyDownloadedSHA512RequiresEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flynn-host-linux-amd64.gz")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	err := verifyDownloadedSHA512(path, "flynn-host-linux-amd64.gz", map[string]string{"other.gz": "abc"})
	if err == nil || !strings.Contains(err.Error(), "no checksum found") {
		t.Fatalf("got %v", err)
	}
	if err := verifyDownloadedSHA512(path, "flynn-host-linux-amd64.gz", nil); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadBinariesChecksumMismatch(t *testing.T) {
	gz := gzipBytes(t, []byte("flynn-host-bin"))
	srv := serveGzipBinaries(t, gz)
	defer srv.Close()

	d := NewWithBaseURL(srv.URL, nil, "vTEST.0", discardLog())
	checksums := gzipChecksums("00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	_, err := d.DownloadBinaries(t.TempDir(), checksums)
	if err == nil {
		t.Fatal("expected hash mismatch")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestDownloadBinariesChecksumMatch(t *testing.T) {
	gz := gzipBytes(t, []byte("flynn-host-bin"))
	srv := serveGzipBinaries(t, gz)
	defer srv.Close()

	d := NewWithBaseURL(srv.URL, nil, "vTEST.0", discardLog())
	paths, err := d.DownloadBinaries(t.TempDir(), gzipChecksums(sha512Hex(gz)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paths["flynn-host"]; !ok {
		t.Fatalf("missing flynn-host path: %#v", paths)
	}
}

func TestDownloadBinariesWithoutChecksums(t *testing.T) {
	gz := gzipBytes(t, []byte("flynn-host-bin"))
	srv := serveGzipBinaries(t, gz)
	defer srv.Close()

	d := NewWithBaseURL(srv.URL, nil, "vTEST.0", discardLog())
	if _, err := d.DownloadBinaries(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha512Hex(b []byte) string {
	sum := sha512.Sum512(b)
	return hex.EncodeToString(sum[:])
}

func gzipChecksums(sum string) map[string]string {
	out := make(map[string]string, len(linuxBinaries()))
	for asset := range linuxBinaries() {
		out[asset+".gz"] = sum
	}
	return out
}

func serveGzipBinaries(t *testing.T, gz []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for asset := range linuxBinaries() {
		name := "/" + asset + ".gz"
		mux.HandleFunc(name, func(w http.ResponseWriter, r *http.Request) {
			w.Write(gz)
		})
	}
	return httptest.NewServer(mux)
}

func discardLog() log15.Logger {
	log := log15.New()
	log.SetHandler(log15.DiscardHandler())
	return log
}

package region

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestNormalizeDefaultsToUS(t *testing.T) {
	for _, in := range []string{"", "  ", "\t"} {
		got, err := NormalizeOrDefault(in)
		if err != nil || got != DefaultName {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	got, err := Normalize("US")
	if err != nil || got != "US" {
		t.Fatalf("US: %q %v", got, err)
	}
	got, err = Normalize("eu-west-1")
	if err != nil || got != "eu-west-1" {
		t.Fatalf("label: %q %v", got, err)
	}
	for _, bad := range []string{"https://eu.example.com", "eu.example.com", "us east", "/US", "1US", ""} {
		if _, err := Normalize(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestBootstrapKeepsBackupRegion(t *testing.T) {
	apply, name, err := BootstrapApply(true, false, "EU")
	if err != nil || apply || name != "" {
		t.Fatalf("from-backup must not write the flag: apply=%v name=%q err=%v", apply, name, err)
	}
	apply, name, err = BootstrapApply(false, true, "EU")
	if err != nil || apply || name != "" {
		t.Fatalf("partial --steps must not write the flag: apply=%v name=%q err=%v", apply, name, err)
	}
	apply, name, err = BootstrapApply(false, false, "")
	if err != nil || !apply || name != "US" {
		t.Fatalf("omitted: apply=%v name=%q err=%v", apply, name, err)
	}
	apply, name, err = BootstrapApply(false, false, "EU")
	if err != nil || !apply || name != "EU" {
		t.Fatalf("explicit: apply=%v name=%q err=%v", apply, name, err)
	}
	if _, _, err := BootstrapApply(false, false, "https://eu.example.com"); err == nil {
		t.Fatal("a URL is not a region label")
	}
}

func TestPrepareLinkRefusesMismatchKeyAndCap(t *testing.T) {
	ok, err := PrepareLink(LinkInput{
		Region: "EU", Domain: "eu.example.com",
		TLSPin: testPin(t), Current: "US", PeerRegion: "EU",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok.ControllerURL != "https://controller.eu.example.com" || ok.DashboardURL != "https://dashboard.eu.example.com" {
		t.Fatalf("urls: %+v", ok)
	}
	raw, err := json.Marshal(ok)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "controller_key") || strings.Contains(string(raw), "cluster_key") || strings.Contains(strings.ToLower(string(raw)), `"key"`) {
		t.Fatalf("link stores a key: %s", raw)
	}

	if _, err := PrepareLink(LinkInput{Region: "EU", Domain: "eu.example.com", TLSPin: testPin(t), Current: "US", PeerRegion: "US"}); err == nil {
		t.Fatal("region mismatch must be refused")
	}
	if _, err := PrepareLink(LinkInput{Region: "US", Domain: "eu.example.com", TLSPin: testPin(t), Current: "US", PeerRegion: "US"}); err == nil {
		t.Fatal("this cluster's region must stay unique among links")
	}
	if _, err := PrepareLink(LinkInput{
		Region: "EU", Domain: "eu.example.com", TLSPin: testPin(t), Current: "US",
		PeerErr: ErrPeerClusterKey,
	}); err != ErrPeerClusterKey {
		t.Fatalf("cluster key offer: %v", err)
	}
	names := make([]string, MaxLinks)
	for i := range names {
		names[i] = "R"
	}
	if _, err := PrepareLink(LinkInput{
		Region: "EU", Domain: "eu.example.com", TLSPin: testPin(t), Current: "US",
		PeerRegion: "EU", Existing: names,
	}); err == nil {
		t.Fatal("link cap must refuse a new region")
	}
	names[0] = "EU"
	if _, err := PrepareLink(LinkInput{
		Region: "EU", Domain: "eu.example.com", TLSPin: testPin(t), Current: "US",
		PeerRegion: "EU", Existing: names,
	}); err != nil {
		t.Fatalf("replacing an existing link at the cap: %v", err)
	}
}

func TestFetchPeerRegionChecksPinAndBody(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/cluster/region" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("mode") {
		case "key":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"region":"EU","controller_key":"secret-cluster-key"}`)
		case "header":
			w.Header().Set("Controller-Key", "secret-cluster-key")
			io.WriteString(w, "EU\n")
		case "us":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "US\n")
		default:
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "EU\n")
		}
	}))
	defer srv.Close()
	pin := pinOf(srv.Certificate())

	got, err := FetchPeerRegion(srv.URL, pin)
	if err != nil || got != "EU" {
		t.Fatalf("pin match: %q %v", got, err)
	}
	if gotAuth != "" || strings.Contains(gotAuth, "CONTROLLER_KEY") {
		t.Fatalf("peer dial sent credentials: %q", gotAuth)
	}
	if _, err := FetchPeerRegion(srv.URL, pinOfBytes([]byte("not-the-cert"))); err == nil {
		t.Fatal("pin mismatch must be refused")
	}
	if _, err := FetchPeerRegion(srv.URL+"?mode=us", pin); err != nil && !strings.Contains(err.Error(), "US") {
		// Fetch returns the name. The caller compares. A different name is still a successful read.
		t.Fatal(err)
	}
	name, err := FetchPeerRegion(srv.URL+"?mode=us", pin)
	if err != nil || name != "US" {
		t.Fatalf("peer name: %q %v", name, err)
	}
	if _, err := PrepareLink(LinkInput{
		Region: "EU", Domain: "eu.example.com", TLSPin: pin, Current: "US", PeerRegion: name,
	}); err == nil {
		t.Fatal("caller must refuse a peer that is not --region")
	}
	if _, err := FetchPeerRegion(srv.URL+"?mode=key", pin); err != ErrPeerClusterKey || (err != nil && strings.Contains(err.Error(), "secret-cluster-key")) {
		t.Fatalf("json cluster key: %v", err)
	}
	if _, err := FetchPeerRegion(srv.URL+"?mode=header", pin); err != ErrPeerClusterKey || (err != nil && strings.Contains(err.Error(), "secret-cluster-key")) {
		t.Fatalf("header cluster key: %v", err)
	}
}

func TestWriteNameIsOnlyTheLabel(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteName(rec, "US")
	if rec.Body.String() != "US\n" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Header().Get("Content-Type")), "json") {
		t.Fatal("peer response must not be JSON")
	}
	for k := range rec.Header() {
		if strings.Contains(strings.ToLower(k), "key") {
			t.Fatalf("response offers a key header %s", k)
		}
	}
}

func TestBlobstoreRegionIsNotThisLabel(t *testing.T) {
	// us-east-1 is a legal cluster label if an operator chooses it, but it is
	// not the default and it is not derived from blobstore:set.
	if DefaultName == "us-east-1" {
		t.Fatal("default region must stay US")
	}
}

func testPin(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte("leaf"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func pinOf(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	return pinOfBytes(cert.Raw)
}

func pinOfBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Keep ct imported so a stored link cannot grow a key field unnoticed.
var _ = ct.RegionLink{}

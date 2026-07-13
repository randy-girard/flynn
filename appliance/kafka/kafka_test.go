package kafka

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderConfig_Plaintext verifies the base broker configuration: the three
// listeners, disabled auto topic creation, and the KRaft quorum voters. It also
// ensures no SSL configuration leaks in when TLS is disabled.
func TestRenderConfig_Plaintext(t *testing.T) {
	p := NewProcess()
	p.NodeID = 7
	p.BootstrapServers = "10.0.0.1:9093"
	p.AdvertisedHost = "10.0.0.1"

	out, err := p.RenderConfig()
	if err != nil {
		t.Fatal(err)
	}

	mustContain(t, out, "node.id=7")
	mustContain(t, out, "controller.quorum.bootstrap.servers=10.0.0.1:9093")
	if strings.Contains(out, "controller.quorum.voters=") {
		t.Fatalf("dynamic quorum must not set controller.quorum.voters:\n%s", out)
	}
	mustContain(t, out, "auto.create.topics.enable=false")
	mustContain(t, out, "inter.broker.listener.name=INTERNAL")
	mustContain(t, out, "listener.security.protocol.map=CONTROLLER:PLAINTEXT,INTERNAL:PLAINTEXT,CLIENT:PLAINTEXT")
	mustContain(t, out, "listeners=CLIENT://:9092,INTERNAL://:9094,CONTROLLER://10.0.0.1:9093")
	mustContain(t, out, "advertised.listeners=CLIENT://10.0.0.1:9092,INTERNAL://10.0.0.1:9094")

	if strings.Contains(out, "ssl.keystore.location") {
		t.Fatalf("did not expect ssl config in plaintext mode:\n%s", out)
	}
}

// TestRenderConfig_TLS verifies the CLIENT listener switches to SSL with mutual
// auth while the internal listeners remain PLAINTEXT.
func TestRenderConfig_TLS(t *testing.T) {
	p := NewProcess()
	p.NodeID = 1
	p.BootstrapServers = "10.0.0.1:9093"
	p.AdvertisedHost = "10.0.0.1"
	p.TLSEnabled = true
	p.KeystorePath = "/data/tls/keystore.p12"
	p.TruststorePath = "/data/tls/truststore.p12"
	p.KeystorePassword = "secret"
	p.TruststorePassword = "secret"

	out, err := p.RenderConfig()
	if err != nil {
		t.Fatal(err)
	}

	mustContain(t, out, "listener.security.protocol.map=CONTROLLER:PLAINTEXT,INTERNAL:PLAINTEXT,CLIENT:SSL")
	mustContain(t, out, "ssl.keystore.location=/data/tls/keystore.p12")
	mustContain(t, out, "ssl.truststore.location=/data/tls/truststore.p12")
	mustContain(t, out, "ssl.client.auth=required")
	mustContain(t, out, "ssl.endpoint.identification.algorithm=")
}

func TestBuildInitialControllers(t *testing.T) {
	// Ordering must be deterministic (sorted by node id) so every broker seeds
	// an identical voter set regardless of discovery order.
	got := BuildInitialControllers(map[int]ControllerInfo{
		30: {Addr: "c:9093", DirectoryID: "d3"},
		10: {Addr: "a:9093", DirectoryID: "d1"},
		20: {Addr: "b:9093", DirectoryID: "d2"},
	})
	want := "10@a:9093:d1,20@b:9093:d2,30@c:9093:d3"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBootstrapServers(t *testing.T) {
	got := BuildBootstrapServers(map[int]ControllerInfo{
		30: {Addr: "c:9093", DirectoryID: "d3"},
		10: {Addr: "a:9093", DirectoryID: "d1"},
		20: {Addr: "b:9093", DirectoryID: "d2"},
	})
	want := "a:9093,b:9093,c:9093"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoadOrCreateDirectoryID(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	gen := func() (string, error) {
		calls++
		return "AAAAAAAAAAAAAAAAAAAAAA", nil
	}

	first, err := LoadOrCreateDirectoryID(dir, gen)
	if err != nil {
		t.Fatal(err)
	}
	if first != "AAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("got %q", first)
	}

	second, err := LoadOrCreateDirectoryID(dir, gen)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("directory id changed across calls: %q != %q", second, first)
	}
	if calls != 1 {
		t.Fatalf("expected gen to be called once, got %d", calls)
	}
}

func TestAssignNodeIDFromBootstrapIDs(t *testing.T) {
	ids := []string{"b", "a", "c"}
	if got := AssignNodeIDFromBootstrapIDs("a", ids); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	if got := AssignNodeIDFromBootstrapIDs("b", ids); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
	if got := AssignNodeIDFromBootstrapIDs("c", ids); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestResolveNodeIDFromMeta(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "kraft-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "meta.properties"), []byte("node.id=42\n"), 0644); err != nil {
		t.Fatal(err)
	}

	id, err := ResolveNodeID(dir, "bootstrap-a", []string{"bootstrap-a", "bootstrap-b"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Fatalf("got %d, want 42", id)
	}
	stored, ok := ReadStoredNodeID(dir)
	if !ok || stored != 42 {
		t.Fatalf("stored node id = %d, ok = %v", stored, ok)
	}
}

func TestResolveNodeIDFromBootstrapIDs(t *testing.T) {
	dir := t.TempDir()
	id, err := ResolveNodeID(dir, "bootstrap-b", []string{"bootstrap-a", "bootstrap-b", "bootstrap-c"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 2 {
		t.Fatalf("got %d, want 2", id)
	}
}

func TestValidateNodeID(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "kraft-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "meta.properties"), []byte("node.id=7\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNodeID(dir, 7); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNodeID(dir, 8); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestAdminArgs(t *testing.T) {
	p := NewProcess()
	p.Port = "9092"

	got := p.adminArgs("--list")
	want := []string{"--bootstrap-server", "localhost:9092", "--list"}
	if !equalStrings(got, want) {
		t.Fatalf("without tls: got %v, want %v", got, want)
	}

	p.CommandConfigPath = "/tmp/client.properties"
	got = p.adminArgs("--list")
	want = []string{"--bootstrap-server", "localhost:9092", "--list", "--command-config", "/tmp/client.properties"}
	if !equalStrings(got, want) {
		t.Fatalf("with tls: got %v, want %v", got, want)
	}
}

func TestClientSecurityProtocol(t *testing.T) {
	p := NewProcess()
	if got := p.ClientSecurityProtocol(); got != "PLAINTEXT" {
		t.Fatalf("got %q, want PLAINTEXT", got)
	}
	p.TLSEnabled = true
	if got := p.ClientSecurityProtocol(); got != "SSL" {
		t.Fatalf("got %q, want SSL", got)
	}
}

// TestGenerateTLSBundle ensures the generated certificates chain to the CA and
// carry the correct extended key usages for mutual TLS.
func TestGenerateTLSBundle(t *testing.T) {
	bundle, err := GenerateTLSBundle([]string{"leader.kafka.discoverd", "localhost"})
	if err != nil {
		t.Fatal(err)
	}

	ca := parseCert(t, bundle.CACert)
	if !ca.IsCA {
		t.Fatal("CA certificate is not marked as a CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	server := parseCert(t, bundle.ServerCert)
	if _, err := server.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("server cert does not verify for server auth: %s", err)
	}

	client := parseCert(t, bundle.ClientCert)
	if _, err := client.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client cert does not verify for client auth: %s", err)
	}
}

func parseCert(t *testing.T, pemData string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		t.Fatal("failed to decode PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected output to contain %q\n---\n%s", needle, haystack)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

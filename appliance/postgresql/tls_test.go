package postgresql

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestEnsureServerTLSGeneratesFiles(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess(Config{
		ID:       "tls-node",
		DataDir:  dir,
		TLSHosts: []string{"postgres.discoverd", "postgres.example.com"},
	})
	if err := p.ensureServerTLS(); err != nil {
		t.Fatal(err)
	}
	if !p.sslEnabled() {
		t.Fatal("expected TLS files")
	}
	if _, err := os.Stat(p.tlsCAPath()); err != nil {
		t.Fatalf("ca: %v", err)
	}
	keyInfo, err := os.Stat(p.tlsKeyPath())
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if keyInfo.Mode().Perm() != 0600 {
		t.Fatalf("server.key mode %o, want 0600", keyInfo.Mode().Perm())
	}
	// second call must not regenerate
	cert, _ := os.ReadFile(p.tlsCertPath())
	if err := p.ensureServerTLS(); err != nil {
		t.Fatal(err)
	}
	cert2, _ := os.ReadFile(p.tlsCertPath())
	if !bytes.Equal(cert, cert2) {
		t.Fatal("existing certs must be reused")
	}
}

func TestConfigTemplateSSLOn(t *testing.T) {
	var buf bytes.Buffer
	err := configTemplate.Execute(&buf, configData{
		Port:        "5432",
		ID:          "primary",
		SSL:         true,
		SSLCertFile: "server.crt",
		SSLKeyFile:  "server.key",
		SSLCAFile:   "root.crt",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"ssl = on", "ssl_cert_file = 'server.crt'", "ssl_key_file = 'server.key'", "ssl_ca_file = 'root.crt'"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestConfigTemplateSSLOff(t *testing.T) {
	var buf bytes.Buffer
	if err := configTemplate.Execute(&buf, configData{Port: "5432", ID: "primary"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ssl = off") {
		t.Fatalf("expected ssl = off, got %s", buf.String())
	}
}

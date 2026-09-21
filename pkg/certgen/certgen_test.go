package certgen

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

func TestGenerateCAAndLeaf(t *testing.T) {
	ca, err := Generate(Params{IsCA: true})
	if err != nil {
		t.Fatal(err)
	}
	parsedCA, err := x509.ParseCertificate(ca.DER)
	if err != nil {
		t.Fatal(err)
	}
	if !parsedCA.IsCA {
		t.Fatal("CA must be a CA")
	}
	if ca.Pin == "" || ca.PEM == "" || ca.KeyPEM == "" {
		t.Fatal("missing PEM/pin")
	}
	sum := sha256.Sum256(ca.DER)
	if ca.Pin != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatal("pin must be base64 sha256 of DER")
	}

	leaf, err := Generate(Params{Hosts: []string{"example.flynnhub.com", "127.0.0.1"}, CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(leaf.DER)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IsCA {
		t.Fatal("leaf must not be a CA")
	}
	if parsed.Subject.CommonName != "example.flynnhub.com" {
		t.Fatalf("CN=%s", parsed.Subject.CommonName)
	}
	if err := parsed.CheckSignatureFrom(parsedCA); err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifyHostname("example.flynnhub.com"); err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatal(err)
	}

	client, err := Generate(Params{Hosts: []string{"client"}, Client: true, CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := x509.ParseCertificate(client.DER)
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.ExtKeyUsage) != 1 || pc.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("client usage=%v", pc.ExtKeyUsage)
	}

	both, err := Generate(Params{Hosts: []string{"db.internal"}, ServerAndClient: true, CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	bc, err := x509.ParseCertificate(both.DER)
	if err != nil {
		t.Fatal(err)
	}
	if len(bc.ExtKeyUsage) != 2 || bc.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || bc.ExtKeyUsage[1] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("server+client usage=%v", bc.ExtKeyUsage)
	}

	block, _ := pem.Decode([]byte(leaf.PEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("leaf PEM")
	}
	keyBlock, _ := pem.Decode([]byte(leaf.KeyPEM))
	if keyBlock == nil || keyBlock.Type != "RSA PRIVATE KEY" {
		t.Fatal("key PEM")
	}
}

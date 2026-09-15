package tlscert

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func TestGenerateClusterCert(t *testing.T) {
	c, err := Generate([]string{"controller.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if c.CACert == "" || c.Cert == "" || c.PrivateKey == "" || c.Pin == "" {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(c.String(), c.Pin) {
		t.Fatalf("String=%s", c.String())
	}
	caBlock, _ := pem.Decode([]byte(c.CACert))
	leafBlock, _ := pem.Decode([]byte(c.Cert))
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("controller.example.com"); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestTLSPinFromDER(t *testing.T) {
	raw := []byte{0x01, 0x02, 0x03, 0x04}
	got := tlsPinFromDER(raw)
	if got == "" {
		t.Fatal("empty pin")
	}
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("pin is not a 32-byte SHA-256: %q %v", got, err)
	}
	if tlsPinFromDER(raw) != got {
		t.Fatal("pin must be deterministic")
	}
	if tlsPinFromDER([]byte{0x01}) == got {
		t.Fatal("different DER must not share a pin")
	}
}

func TestControllerTLSAddr(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://controller.example.com", "controller.example.com:443"},
		{"https://controller.example.com:8443/path", "controller.example.com:8443"},
		{"https://127.0.0.1", "127.0.0.1:443"},
		{"https://[::1]/", "[::1]:443"},
		{"https://[2001:db8::1]:9443", "[2001:db8::1]:9443"},
	}
	for _, tc := range cases {
		got, err := controllerTLSAddr(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q %v want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := controllerTLSAddr("://"); err == nil {
		t.Fatal("want parse error")
	}
	if _, err := controllerTLSAddr("https:///nohost"); err == nil {
		t.Fatal("want missing host")
	}
}

func TestConfirmTLSPinRefresh(t *testing.T) {
	prompted := 0
	deny := func(string) bool { prompted++; return false }
	allow := func(string) bool { prompted++; return true }

	if err := confirmTLSPinRefresh(true, false, deny, "unused"); err != nil {
		t.Fatalf("--yes must not require a TTY: %v", err)
	}
	if prompted != 0 {
		t.Fatal("--yes must not prompt")
	}

	err := confirmTLSPinRefresh(false, false, deny, "unused")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive without --yes: %v", err)
	}
	if prompted != 0 {
		t.Fatal("non-interactive must not prompt")
	}

	if err := confirmTLSPinRefresh(false, true, allow, "store pin?"); err != nil {
		t.Fatalf("interactive yes: %v", err)
	}
	if prompted != 1 {
		t.Fatalf("prompted=%d", prompted)
	}

	err = confirmTLSPinRefresh(false, true, deny, "store pin?")
	if !errors.Is(err, errTLSPinRefreshAborted) {
		t.Fatalf("want aborted, got %v", err)
	}
}

func TestTLSPinRefreshSummaryAndPrompt(t *testing.T) {
	leaf := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "controller.example.com"},
		Issuer:   pkix.Name{CommonName: "Flynn CA"},
		NotAfter: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	out := tlsPinRefreshSummary("default", "oldpin", "newpin", leaf, false)
	for _, want := range []string{
		`cluster "default"`,
		"Certificate Subject: controller.example.com",
		"Certificate Issuer: Flynn CA",
		"Valid Until: 2030-01-02T03:04:05Z",
		"Old Pin: oldpin",
		"New Pin: newpin",
		"TLS verification: skipped",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q in:\n%s", want, out)
		}
	}
	out = tlsPinRefreshSummary("prod", "", "newpin", leaf, true)
	if !strings.Contains(out, "Old Pin: (none)") || !strings.Contains(out, "system CAs") {
		t.Fatalf("verified empty-old summary:\n%s", out)
	}
	if msg := tlsPinRefreshPrompt("default", true); !strings.Contains(msg, "verified against system CAs") {
		t.Fatalf("verified prompt: %s", msg)
	}
	if msg := tlsPinRefreshPrompt("default", false); !strings.Contains(msg, "NOT verified") {
		t.Fatalf("unverified prompt: %s", msg)
	}
}

func TestDialTLSPinVerifiedThenInsecureFallback(t *testing.T) {
	caTLS, caCert, caKey := mustTestCA(t, "Test CA")
	_ = caTLS
	leafTLS, leafCert, _ := mustTestLeaf(t, "controller.test", []string{"controller.test"}, nil, caCert, caKey)

	addr, stop := startTLSListener(t, leafTLS)
	defer stop()

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	conn, verified, err := dialTLSPin(addr, &tls.Config{RootCAs: pool, ServerName: "controller.test"}, &tls.Config{InsecureSkipVerify: false, RootCAs: x509.NewCertPool()})
	if err != nil {
		t.Fatalf("verified dial: %v", err)
	}
	defer conn.Close()
	if !verified {
		t.Fatal("publicly signed cert must use the verified handshake")
	}
	pin, gotLeaf, err := tlsPinFromConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	if pin != tlsPinFromDER(leafCert.Raw) {
		t.Fatalf("pin=%s want %s", pin, tlsPinFromDER(leafCert.Raw))
	}
	if gotLeaf.Subject.CommonName != "controller.test" {
		t.Fatalf("cn=%s", gotLeaf.Subject.CommonName)
	}

	// Self-signed: verified handshake fails, insecure fallback succeeds.
	selfTLS, selfCert, _ := mustTestLeaf(t, "self.example", []string{"self.example"}, []net.IP{net.ParseIP("127.0.0.1")}, nil, nil)
	addr2, stop2 := startTLSListener(t, selfTLS)
	defer stop2()
	conn2, verified2, err := dialTLSPin(addr2, &tls.Config{ServerName: "127.0.0.1"}, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("insecure fallback: %v", err)
	}
	defer conn2.Close()
	if verified2 {
		t.Fatal("self-signed cert must not count as verified")
	}
	pin2, _, err := tlsPinFromConn(conn2)
	if err != nil {
		t.Fatal(err)
	}
	if pin2 != tlsPinFromDER(selfCert.Raw) {
		t.Fatalf("self pin=%s", pin2)
	}

	_, _, err = dialTLSPin(addr2, &tls.Config{ServerName: "127.0.0.1"}, &tls.Config{InsecureSkipVerify: false})
	if err == nil {
		t.Fatal("both handshakes failing must surface an error")
	}
}

func startTLSListener(t *testing.T, cert tls.Certificate) (addr string, stop func()) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func mustTestCA(t *testing.T, cn string) (tls.Certificate, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	return mustTestCert(t, cn, nil, nil, nil, nil, true)
}

func mustTestLeaf(t *testing.T, cn string, dns []string, ips []net.IP, parent *x509.Certificate, parentKey crypto.Signer) (tls.Certificate, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	return mustTestCert(t, cn, dns, ips, parent, parentKey, false)
}

func mustTestCert(t *testing.T, cn string, dns []string, ips []net.IP, parent *x509.Certificate, parentKey crypto.Signer, isCA bool) (tls.Certificate, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"flynn-test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              dns,
		IPAddresses:           ips,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	signerCert := tmpl
	signerKey := crypto.Signer(key)
	if parent != nil {
		signerCert = parent
		signerKey = parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
	if parent != nil {
		tlsCert.Certificate = append(tlsCert.Certificate, parent.Raw)
	}
	return tlsCert, parsed, key
}

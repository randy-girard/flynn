package tlsconfig

import (
	"crypto/tls"
	"testing"
)

func TestSecureCiphers(t *testing.T) {
	got := SecureCiphers(nil)
	if got.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion=%d", got.MinVersion)
	}
	if len(got.CipherSuites) == 0 {
		t.Fatal("empty ciphers")
	}
	for _, c := range got.CipherSuites {
		switch c {
		case tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA:
			t.Fatalf("insecure cipher 0x%x", c)
		}
	}
	if len(got.CurvePreferences) != 2 || got.CurvePreferences[0] != tls.X25519 || got.CurvePreferences[1] != tls.CurveP256 {
		t.Fatalf("curves=%v", got.CurvePreferences)
	}

	in := &tls.Config{MinVersion: tls.VersionTLS10, ServerName: "example"}
	out := SecureCiphers(in)
	if out != in {
		t.Fatal("must update the provided config in place")
	}
	if in.MinVersion != tls.VersionTLS12 || in.ServerName != "example" {
		t.Fatalf("%+v", in)
	}
}

package verify

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestNewVerifierRejectsBadInputs(t *testing.T) {
	if _, err := NewVerifier(map[string]string{"sha256": "abc"}, 0); err == nil {
		t.Fatal("size 0")
	}
	if _, err := NewVerifier(map[string]string{"md5": "abc"}, 4); err != ErrNoHashes {
		t.Fatalf("unknown alg: %v", err)
	}
	if _, err := NewVerifier(nil, 4); err != ErrNoHashes {
		t.Fatalf("empty hashes: %v", err)
	}
}

func TestVerifierSHA256AndMismatch(t *testing.T) {
	payload := []byte("flynn-layer-bytes")
	sum := sha256.Sum256(payload)
	v, err := NewVerifier(map[string]string{
		"sha256": hex.EncodeToString(sum[:]),
		"md5":    "ignored",
	}, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, v.Reader(bytes.NewReader(payload))); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(); err != nil {
		t.Fatal(err)
	}

	bad, err := NewVerifier(map[string]string{"sha256": strings.Repeat("0", 64)}, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, bad.Reader(bytes.NewReader(payload)))
	err = bad.Verify()
	if _, ok := err.(*ErrHashMismatch); !ok {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestVerifierShortDataAndSHA512(t *testing.T) {
	payload := []byte("abcdef")
	v, err := NewVerifier(map[string]string{"sha256": "00"}, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(); err != ErrShortData {
		t.Fatalf("no reader: %v", err)
	}
	io.Copy(io.Discard, v.Reader(bytes.NewReader(payload[:3])))
	if err := v.Verify(); err != ErrShortData {
		t.Fatalf("short: %v", err)
	}

	sum := sha512.Sum512(payload)
	sum256 := sha512.Sum512_256(payload)
	v, err = NewVerifier(map[string]string{
		"sha512":     hex.EncodeToString(sum[:]),
		"sha512_256": hex.EncodeToString(sum256[:]),
	}, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, v.Reader(bytes.NewReader(append(payload, 'X'))))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("must stop at declared size, got %d", n)
	}
	if err := v.Verify(); err != nil {
		t.Fatal(err)
	}
}

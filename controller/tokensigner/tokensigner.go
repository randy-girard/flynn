// Package tokensigner is the private-key counterpart of
// controller/authorizer: it mints signed AccessTokens that authorizer can
// verify. Only trusted components (e.g. the gitreceive receiver) hold a
// signing key; everything else only ever verifies with the public key.
package tokensigner

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"

	api "github.com/flynn/flynn/controller/api"
	"google.golang.org/protobuf/proto"
)

// GenerateKeyPair creates a new ECDSA P-256 keypair for signing and verifying
// build access tokens. The returned strings are base64url-encoded PKIX public
// and PKCS#8 private keys matching authorizer.ParseTokenKey and
// ParseSigningKey respectively.
func GenerateKeyPair() (publicKey, privateKey string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	return base64.URLEncoding.EncodeToString(pub),
		base64.URLEncoding.EncodeToString(privDER),
		nil
}

// Signer mints signed AccessTokens using an ECDSA private key. The produced
// tokens are verifiable by controller/authorizer using the corresponding
// public key (ACCESS_TOKEN_KEY).
type Signer struct {
	key *ecdsa.PrivateKey
}

// ParseSigningKey decodes a base64url-encoded PKCS#8 ECDSA private key, the
// private-key counterpart of authorizer.ParseTokenKey. An empty string returns
// a nil signer so callers can treat "no signing key configured" as disabled.
func ParseSigningKey(sk string) (*Signer, error) {
	if sk == "" {
		return nil, nil
	}
	b, err := base64.URLEncoding.DecodeString(sk)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(b)
	if err != nil {
		return nil, err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("unexpected signing key type %T, want *ecdsa.PrivateKey", k)
	}
	return &Signer{key: key}, nil
}

// New returns a Signer wrapping an already-parsed private key.
func New(key *ecdsa.PrivateKey) *Signer {
	return &Signer{key: key}
}

// Sign marshals the AccessToken, signs SHA-256(data) with ECDSA (ASN.1 DER
// signature, matching authorizer.verifyASN1), wraps the pair in a SignedData
// envelope, and returns the base64url-encoded result suitable for use as a
// Bearer token. The encoding mirrors authorizer.AuthorizeToken so a token
// produced here round-trips through verification unchanged.
func (s *Signer) Sign(token *api.AccessToken) (string, error) {
	if s == nil || s.key == nil {
		return "", fmt.Errorf("no signing key configured")
	}

	data, err := proto.Marshal(token)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, hash[:])
	if err != nil {
		return "", err
	}

	signed, err := proto.Marshal(&api.SignedData{Data: data, Signature: sig})
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(signed), nil
}

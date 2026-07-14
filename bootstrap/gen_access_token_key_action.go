package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
)

func init() {
	Register("gen-access-token-key", &GenAccessTokenKeyAction{})
}

// GenAccessTokenKeyAction generates the ECDSA P-256 keypair used to sign and
// verify build access tokens. The public half (PublicKey) is distributed to the
// controller and tarreceive as ACCESS_TOKEN_KEY so they can verify tokens; the
// private half (PrivateKey) is distributed only to the trusted gitreceive app
// as ACCESS_TOKEN_SIGNING_KEY so it alone can mint short-lived, app-scoped
// build tokens. See docs/plans/scoped-build-tokens.md.
type GenAccessTokenKeyAction struct {
	ID string `json:"id"`
}

// AccessTokenKeyData holds the base64url-encoded keypair for use in later
// manifest steps via template lookups on .StepData.
type AccessTokenKeyData struct {
	// PublicKey is the base64url PKIX-encoded public key (ACCESS_TOKEN_KEY),
	// matching authorizer.ParseTokenKey.
	PublicKey string `json:"public_key"`
	// PrivateKey is the base64url PKCS#8-encoded private key
	// (ACCESS_TOKEN_SIGNING_KEY), matching tokensigner.ParseSigningKey.
	PrivateKey string `json:"private_key"`
}

func (d *AccessTokenKeyData) String() string {
	return d.PublicKey
}

func (a *GenAccessTokenKeyAction) Run(s *State) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	s.StepData[a.ID] = &AccessTokenKeyData{
		PublicKey:  base64.URLEncoding.EncodeToString(pub),
		PrivateKey: base64.URLEncoding.EncodeToString(privDER),
	}
	return nil
}

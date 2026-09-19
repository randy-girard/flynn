package githubapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseRSAPrivateKey reads a GitHub App PEM (PKCS#1 or PKCS#8 RSA).
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("no PEM private key found")
		}
		switch block.Type {
		case "RSA PRIVATE KEY":
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse PKCS#1 private key: %w", err)
			}
			return key, nil
		case "PRIVATE KEY":
			parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
			}
			key, ok := parsed.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("PKCS#8 key is not RSA")
			}
			return key, nil
		}
	}
}

// SignAppJWT builds a GitHub App JWT (RS256, iss=app id, iat/exp in seconds).
func SignAppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	if appID <= 0 {
		return "", fmt.Errorf("github app id is required")
	}
	if key == nil {
		return "", fmt.Errorf("github app private key is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	header, err := jwtEncodeJSON(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := jwtEncodeJSON(map[string]interface{}{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + payload
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func jwtEncodeJSON(v interface{}) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeJWTPayload is for tests: return the JSON payload of a compact JWT.
func DecodeJWTPayload(token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT")
	}
	return base64.RawURLEncoding.DecodeString(parts[1])
}

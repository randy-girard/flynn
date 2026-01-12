package acme

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	ct "github.com/flynn/flynn/controller/types"
)

// Account represents an ACME account
type Account struct {
	// Key is the PEM-encoded private key for the account
	Key string `json:"key,omitempty"`
	// Contacts are the contact email addresses for the account
	Contacts []string `json:"contacts,omitempty"`
	// TermsOfServiceAgreed indicates whether the ToS have been agreed to
	TermsOfServiceAgreed bool `json:"terms_of_service_agreed,omitempty"`
}

// NewAccountFromConfig creates an Account from an ACMEConfig
func NewAccountFromConfig(config *ct.ACMEConfig) (*Account, error) {
	if config == nil {
		return nil, fmt.Errorf("ACME configuration is nil")
	}
	if !config.Enabled {
		return nil, fmt.Errorf("ACME is not enabled")
	}
	if config.AccountKey == "" {
		return nil, fmt.Errorf("ACME account key is not configured")
	}
	account := &Account{
		Key:                  config.AccountKey,
		TermsOfServiceAgreed: config.TermsOfServiceAgreed,
	}
	if config.ContactEmail != "" {
		account.Contacts = []string{config.ContactEmail}
	}
	return account, nil
}

// PrivateKey parses and returns the account's private key
func (a *Account) PrivateKey() (*ecdsa.PrivateKey, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("account key is empty")
	}
	block, _ := pem.Decode([]byte(a.Key))
	if block == nil {
		return nil, fmt.Errorf("failed to parse PEM block")
	}
	privKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse EC private key: %s", err)
	}
	return privKey, nil
}

// KeyID returns a unique identifier for the account key
func (a *Account) KeyID() string {
	if a.Key == "" {
		return ""
	}
	// Return first 8 chars of key for identification
	if len(a.Key) > 50 {
		return a.Key[27:35]
	}
	return "unknown"
}

package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	acmelib "github.com/eggsampler/acme/v3"
)

// GenACMEAccountAction generates an ACME account for Let's Encrypt
type GenACMEAccountAction struct {
	ID string `json:"id"`

	// DirectoryURL is the ACME directory URL (defaults to Let's Encrypt production)
	DirectoryURL string `json:"directory_url"`
	// Contacts are the contact email addresses for the account
	Contacts []string `json:"contacts"`
	// TermsOfServiceAgreed indicates whether the ToS have been agreed to
	TermsOfServiceAgreed bool `json:"terms_of_service_agreed"`

	// Key is an optional pre-existing PEM-encoded private key
	Key string `json:"key"`
}

func init() {
	Register("gen-acme-account", &GenACMEAccountAction{})
}

// ACMEAccountData holds the generated ACME account data
type ACMEAccountData struct {
	// Key is the PEM-encoded private key for the account
	Key string `json:"key"`
	// Contacts are the contact email addresses for the account
	Contacts []string `json:"contacts"`
	// TermsOfServiceAgreed indicates whether the ToS have been agreed to
	TermsOfServiceAgreed bool `json:"terms_of_service_agreed"`
	// DirectoryURL is the ACME directory URL
	DirectoryURL string `json:"directory_url"`
}

func (a *GenACMEAccountAction) Run(s *State) error {
	data := &ACMEAccountData{
		Contacts:             a.Contacts,
		TermsOfServiceAgreed: a.TermsOfServiceAgreed,
		DirectoryURL:         a.DirectoryURL,
	}
	s.StepData[a.ID] = data

	// Interpolate values
	a.Key = interpolate(s, a.Key)
	a.DirectoryURL = interpolate(s, a.DirectoryURL)
	for i, c := range a.Contacts {
		a.Contacts[i] = interpolate(s, c)
	}

	// Use default directory URL if not specified
	if a.DirectoryURL == "" {
		a.DirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"
	}
	data.DirectoryURL = a.DirectoryURL

	// If key is provided, use it
	if a.Key != "" {
		data.Key = a.Key
		return nil
	}

	// Generate a new ECDSA key
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("error generating ACME account key: %s", err)
	}

	// Create ACME client and register account
	client, err := acmelib.NewClient(a.DirectoryURL)
	if err != nil {
		return fmt.Errorf("error creating ACME client: %s", err)
	}

	// Format contacts as mailto: URLs
	contacts := make([]string, len(a.Contacts))
	for i, c := range a.Contacts {
		if !strings.HasPrefix(c, "mailto:") {
			contacts[i] = "mailto:" + c
		} else {
			contacts[i] = c
		}
	}

	// Register the account
	_, err = client.NewAccount(privKey, false, a.TermsOfServiceAgreed, contacts...)
	if err != nil {
		return fmt.Errorf("error registering ACME account: %s", err)
	}

	// Encode the private key
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("error encoding private key: %s", err)
	}
	data.Key = string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	}))

	return nil
}


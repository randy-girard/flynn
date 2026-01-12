package data

import (
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/postgres"
)

type ACMEConfigRepo struct {
	db *postgres.DB
}

func NewACMEConfigRepo(db *postgres.DB) *ACMEConfigRepo {
	return &ACMEConfigRepo{db: db}
}

// Get returns the current ACME configuration
func (r *ACMEConfigRepo) Get() (*ct.ACMEConfig, error) {
	config := &ct.ACMEConfig{}
	// Use pointers to handle NULL values from the database
	var contactEmail, directoryURL, accountKey *string
	err := r.db.QueryRow("acme_config_select").Scan(
		&config.Enabled,
		&contactEmail,
		&directoryURL,
		&config.TermsOfServiceAgreed,
		&accountKey,
		&config.CreatedAt,
		&config.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	// Convert pointers to strings (empty string for NULL)
	if contactEmail != nil {
		config.ContactEmail = *contactEmail
	}
	if directoryURL != nil {
		config.DirectoryURL = *directoryURL
	}
	if accountKey != nil {
		config.AccountKey = *accountKey
	}
	return config, nil
}

// Update updates the ACME configuration
func (r *ACMEConfigRepo) Update(config *ct.ACMEConfig) error {
	return r.db.QueryRow("acme_config_update",
		config.Enabled,
		config.ContactEmail,
		config.DirectoryURL,
		config.TermsOfServiceAgreed,
		config.AccountKey,
	).Scan(&config.UpdatedAt)
}

// IsEnabled returns whether ACME is enabled
func (r *ACMEConfigRepo) IsEnabled() (bool, error) {
	config, err := r.Get()
	if err != nil {
		return false, err
	}
	return config.Enabled, nil
}

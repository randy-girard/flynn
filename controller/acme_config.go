package main

import (
	"net/http"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// GetACMEConfig returns the current ACME configuration
func (c *controllerAPI) GetACMEConfig(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	config, err := c.acmeConfigRepo.Get()
	if err != nil {
		respondWithError(w, err)
		return
	}
	// Set HasAccountKey before stripping the key for security
	config.HasAccountKey = config.AccountKey != ""
	// Check if the request includes the internal header
	if req.Header.Get("X-Flynn-Internal") != "true" {
		config.AccountKey = ""
	}
	httphelper.JSON(w, 200, config)
}

// UpdateACMEConfig updates the ACME configuration
func (c *controllerAPI) UpdateACMEConfig(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var newConfig ct.ACMEConfig
	if err := httphelper.DecodeJSON(req, &newConfig); err != nil {
		respondWithError(w, err)
		return
	}

	// Get existing config to preserve account key if not provided
	existingConfig, err := c.acmeConfigRepo.Get()
	if err != nil {
		respondWithError(w, err)
		return
	}

	// Preserve account key if not provided in the request
	if newConfig.AccountKey == "" {
		newConfig.AccountKey = existingConfig.AccountKey
	}

	// Validate required fields when enabling ACME
	if newConfig.Enabled {
		if newConfig.ContactEmail == "" {
			respondWithError(w, ct.ValidationError{
				Field:   "contact_email",
				Message: "contact email is required when enabling ACME",
			})
			return
		}
		if !newConfig.TermsOfServiceAgreed {
			respondWithError(w, ct.ValidationError{
				Field:   "terms_of_service_agreed",
				Message: "you must agree to the Let's Encrypt terms of service",
			})
			return
		}
	}

	if err := c.acmeConfigRepo.Update(&newConfig); err != nil {
		respondWithError(w, err)
		return
	}

	// Don't expose the private key in the response
	newConfig.AccountKey = ""
	httphelper.JSON(w, 200, &newConfig)
}

// IsACMEEnabled checks if ACME is enabled globally
func (c *controllerAPI) IsACMEEnabled() (bool, error) {
	return c.acmeConfigRepo.IsEnabled()
}

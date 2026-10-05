package main

import (
	"log"
	"os"
	"strings"

	"github.com/randy-girard/flynn/controller/tokensigner"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// tokenSignerFromEnvAndDB loads the ECDSA key used to mint user access tokens.
// New clusters set ACCESS_TOKEN_SIGNING_KEY on the controller. Older clusters
// only stored the private half on gitreceive (build tokens); login still needs
// that same key so the controller public ACCESS_TOKEN_KEY can verify it.
func tokenSignerFromEnvAndDB(db *postgres.DB) (*tokensigner.Signer, error) {
	s, err := tokensigner.ParseSigningKey(firstNonEmpty(
		os.Getenv("ACCESS_TOKEN_SIGNING_KEY"),
		os.Getenv("ACCESS_TOKEN_PRIVATE_KEY"),
	))
	if err != nil {
		return nil, err
	}
	if s != nil {
		return s, nil
	}
	for _, app := range []string{"gitreceive", "controller"} {
		sk := appReleaseSigningKey(db, app)
		if sk == "" {
			continue
		}
		s, err = tokensigner.ParseSigningKey(sk)
		if err != nil {
			log.Printf("controller: ignoring %s ACCESS_TOKEN_SIGNING_KEY: %v", app, err)
			continue
		}
		if s != nil {
			log.Printf("controller: using ACCESS_TOKEN_SIGNING_KEY from %s release", app)
			return s, nil
		}
	}
	log.Println("controller: ACCESS_TOKEN_SIGNING_KEY is not set; flynn login cannot mint tokens")
	return nil, nil
}

func appReleaseSigningKey(db *postgres.DB, appName string) string {
	if db == nil || strings.TrimSpace(appName) == "" {
		return ""
	}
	var key string
	err := db.QueryRow(`
		SELECT COALESCE(r.env->>'ACCESS_TOKEN_SIGNING_KEY', r.env->>'ACCESS_TOKEN_PRIVATE_KEY', '')
		FROM apps a
		JOIN releases r ON r.release_id = a.release_id
		WHERE a.deleted_at IS NULL AND a.name = $1
	`, appName).Scan(&key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(key)
}

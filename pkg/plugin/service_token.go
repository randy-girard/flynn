package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/randy-girard/flynn/controller/servicetoken"
	"github.com/randy-girard/flynn/controller/tokensigner"
)

// ApplyServiceToken sets spec.Env from the previous release or a new mint,
// records a hash of the cluster key when asked, then deletes spec.Strip.
// A missing signing key fails the install. It does not leave CONTROLLER_KEY
// in place as a fallback.
func ApplyServiceToken(env, previous, cluster map[string]string, spec *ServiceToken) error {
	if spec == nil || strings.TrimSpace(spec.Env) == "" {
		return nil
	}
	if env == nil {
		return fmt.Errorf("service token %s: release env is nil", spec.Env)
	}
	name := strings.TrimSpace(spec.Env)
	if previous != nil && strings.TrimSpace(previous[name]) != "" {
		env[name] = previous[name]
	} else {
		raw := ""
		if cluster != nil {
			raw = cluster["ACCESS_TOKEN_SIGNING_KEY"]
		}
		signer, err := tokensigner.ParseSigningKey(raw)
		if err != nil {
			return fmt.Errorf("service token %s: %w", name, err)
		}
		tok, err := servicetoken.Mint(signer, spec.Scope, spec.Caller, servicetoken.DefaultTTL)
		if err != nil {
			return fmt.Errorf("service token %s: %w", name, err)
		}
		env[name] = tok
	}
	if hashEnv := strings.TrimSpace(spec.AuthHashEnv); hashEnv != "" && env[hashEnv] == "" {
		key := env["CONTROLLER_KEY"]
		if key == "" && previous != nil {
			key = previous["CONTROLLER_KEY"]
		}
		if key != "" {
			sum := sha256.Sum256([]byte(key))
			env[hashEnv] = hex.EncodeToString(sum[:])
		}
	}
	for _, k := range spec.Strip {
		delete(env, k)
	}
	if strings.TrimSpace(env[name]) == "" {
		return fmt.Errorf("service token %s was not set", name)
	}
	return nil
}

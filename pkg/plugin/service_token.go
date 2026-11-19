package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/servicetoken"
	"github.com/randy-girard/flynn/controller/tokensigner"
)

// ApplyServiceToken sets spec.Env from the previous release when that token
// is still valid for spec.Scope and not near expiry, otherwise from a new
// mint (servicetoken.RenewBefore),
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
	reuse := false
	if previous != nil && strings.TrimSpace(previous[name]) != "" {
		reuse, _ = servicetoken.Reusable(previous[name], spec.Scope, time.Now())
	}
	if reuse {
		env[name] = previous[name]
	} else {
		// Missing, unparseable, wrong scope, expired, or within the renewal
		// window: mint a new one. Drop any stale copy PreservePreviousEnv made.
		delete(env, name)
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
	if hashEnv := strings.TrimSpace(spec.AuthHashEnv); hashEnv != "" {
		key := env["CONTROLLER_KEY"]
		if key == "" && previous != nil {
			key = previous["CONTROLLER_KEY"]
		}
		if key != "" {
			// Always recomputed so a rotated cluster key is not left behind
			// a stale hash. Without a key, keep the existing value.
			sum := sha256.Sum256([]byte(key))
			env[hashEnv] = hex.EncodeToString(sum[:])
		} else if env[hashEnv] == "" && previous != nil {
			env[hashEnv] = previous[hashEnv]
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

package authz

import (
	"strings"

	"github.com/flynn/flynn/controller/authorizer"
)

// GRPCAllowed returns whether the gRPC method may run with this principal.
// App-scoped dashboard tokens currently allow only lightweight health checks —
// controller HTTP is the integration surface for narrowly scoped collaborators.
func GRPCAllowed(tok *authorizer.Token, fullMethod string) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	if tok.BearerScopedToApps() {
		return strings.HasSuffix(fullMethod, "Controller/Status")
	}
	return false
}

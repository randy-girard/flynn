package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
)

var (
	errCallerUnauthorized = errors.New("controller did not accept the credential")
	errCallerForbidden    = errors.New("controller refused the caller")
)

// callerLookup asks the controller for the grants of the credential on r.
type callerLookup interface {
	CallerGrants(r *http.Request) (*ct.CallerGrants, error)
}

// controllerCallers is callerLookup over GET /whoami/grants. It sends the
// caller's own Authorization header and nothing else, so the controller
// authenticates the caller (login token or personal access token) and
// expands its grants exactly as it does for every other request.
type controllerCallers struct {
	url    string
	client *http.Client
}

const callerGrantsTimeout = 15 * time.Second

func newControllerCallers(controllerURL string) *controllerCallers {
	return &controllerCallers{url: strings.TrimRight(controllerURL, "/"), client: &http.Client{Timeout: callerGrantsTimeout}}
}

func (c *controllerCallers) CallerGrants(r *http.Request) (*ct.CallerGrants, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, errCallerUnauthorized
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.url+"/whoami/grants", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, errCallerUnauthorized
	case http.StatusForbidden:
		return nil, errCallerForbidden
	default:
		return nil, fmt.Errorf("GET /whoami/grants: unexpected status %d", res.StatusCode)
	}
	var g ct.CallerGrants
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&g); err != nil {
		return nil, fmt.Errorf("GET /whoami/grants: %w", err)
	}
	return &g, nil
}

// presentsPAT reports whether the request carries a personal access token.
// gitreceive cannot check one itself; only the controller holds the hashes.
func presentsPAT(r *http.Request) bool {
	secret := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		secret = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	} else {
		_, secret, _ = r.BasicAuth()
	}
	return strings.HasPrefix(secret, "flynn_pat_")
}

// authorizeCaller returns the principal for a git request. The cluster key,
// a cluster:admin token, and an app-scoped token (dashboard, build) are
// decided from the credential alone. A user's login token names the user and
// carries no grants: the controller derives them per request from the apps
// the user's account can reach. gitreceive asks the controller for those
// grants and checks the app by id, as the controller would. A personal access
// token is only known to the controller, so it is resolved the same way.
func (h *gitHandler) authorizeCaller(r *http.Request) (*authorizer.Token, error) {
	tok, err := h.auth.AuthorizeRequest(r)
	if err != nil {
		if h.callers == nil || !presentsPAT(r) {
			return nil, errCallerUnauthorized
		}
		g, err := h.callers.CallerGrants(r)
		if err != nil {
			return nil, callerError(err)
		}
		if g.UserID == "" {
			return nil, errCallerUnauthorized
		}
		pat := &authorizer.Token{UserID: g.UserID}
		applyCallerGrants(pat, g)
		return pat, nil
	}
	if h.callers == nil || !authz.UserBound(tok) || tok.HasClusterAdmin() {
		return tok, nil
	}
	g, err := h.callers.CallerGrants(r)
	if err != nil {
		if errors.Is(err, errCallerForbidden) || errors.Is(err, errCallerUnauthorized) {
			return nil, err
		}
		// Controller unreachable: keep the token's own grants. That fails
		// closed for a tenant, whose login token has none.
		log.Printf("gitreceive: caller grants for user %s: %v", tok.UserID, err)
		return tok, nil
	}
	if g.UserID != tok.UserID {
		log.Printf("gitreceive: caller grants answered for user %q, token names %q; ignoring", g.UserID, tok.UserID)
		return tok, nil
	}
	applyCallerGrants(tok, g)
	return tok, nil
}

func callerError(err error) error {
	if errors.Is(err, errCallerForbidden) {
		return err
	}
	if !errors.Is(err, errCallerUnauthorized) {
		log.Printf("gitreceive: caller grants: %v", err)
	}
	return errCallerUnauthorized
}

// applyCallerGrants adds the controller's grants to tok. They are keyed by
// app id; the push and fetch checks match on app.ID only, so a grant keyed by
// anything else never matches.
func applyCallerGrants(tok *authorizer.Token, g *ct.CallerGrants) {
	if g.ClusterAdmin {
		tok.Scopes = append(tok.Scopes, "cluster:admin")
	}
	for _, ag := range g.AppGrants {
		if ag.AppID == "" || len(ag.Permissions) == 0 {
			continue
		}
		tok.AppGrants = append(tok.AppGrants, authorizer.AppGrant{AppID: ag.AppID, Permissions: append([]string(nil), ag.Permissions...)})
	}
}

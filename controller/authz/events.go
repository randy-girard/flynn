package authz

import (
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
)

// ScopedRejection is what gets logged when a scoped token is denied.
// It never carries the token bytes.
type ScopedRejection struct {
	Scope  string
	Caller string
	Method string
	Path   string
	Reason string
}

// OnScopedReject is optional. The controller also writes a log line.
// Tests set this to prove the token itself is not recorded.
var OnScopedReject func(ScopedRejection)

// NoteScopedReject records scope and caller for a denied scoped token.
func NoteScopedReject(tok *authorizer.Token, method, path, reason string) {
	if tok == nil || OnScopedReject == nil {
		return
	}
	caller := strings.TrimSpace(tok.UserID)
	if caller == "" {
		caller = strings.TrimSpace(tok.User)
	}
	OnScopedReject(ScopedRejection{
		Scope:  strings.Join(tok.Scopes, ","),
		Caller: caller,
		Method: method,
		Path:   path,
		Reason: reason,
	})
}

func hasScope(tok *authorizer.Token, scope string) bool {
	if tok == nil {
		return false
	}
	for _, s := range tok.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func hasEventsGrant(tok *authorizer.Token) bool {
	for _, g := range tok.AppGrants {
		if g.AppID == "" {
			continue
		}
		for _, p := range g.Permissions {
			if p == ScopeEventsRead {
				return true
			}
		}
	}
	return false
}

// EventsGrantAllows reports whether tok may read events for this app.
// A user id prefixed with events: is the dashboard's per-app watcher and one
// prefixed with build: is a compile credential's rate-limit identity; neither
// is an account binding (the grant names the one app). Any other user id must match the app owner when
// the app has one, so a grant copied onto another account's app is still denied.
func EventsGrantAllows(tok *authorizer.Token, appID, appName, ownerAccount string) bool {
	if !hasScope(tok, ScopeEventsRead) || !eventsGrantMatches(tok, appID, appName) {
		return false
	}
	if accountBound(tok.UserID) && strings.TrimSpace(ownerAccount) != "" && !sameAccount(tok.UserID, ownerAccount) {
		return false
	}
	return true
}

func eventsGrantMatches(tok *authorizer.Token, appID, appName string) bool {
	if tok == nil {
		return false
	}
	for _, g := range tok.AppGrants {
		if g.AppID == "" || g.AppID != appID {
			continue
		}
		for _, p := range g.Permissions {
			if p == ScopeEventsRead {
				return true
			}
		}
	}
	return false
}

func accountBound(userID string) bool {
	id := strings.TrimSpace(userID)
	if id == "" || strings.HasPrefix(id, "events:") || strings.HasPrefix(id, "build:") {
		return false
	}
	return true
}

func sameAccount(userID, owner string) bool {
	userID = strings.TrimPrefix(strings.TrimSpace(userID), "user:")
	owner = strings.TrimPrefix(strings.TrimSpace(owner), "user:")
	return userID != "" && userID == owner
}

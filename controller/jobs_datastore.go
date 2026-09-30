package main

import (
	"crypto/subtle"
	"net/http"
	"os"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// refuseDatastoreRunJob rejects Flynn CLI one-offs on database apps. Native
// clients (psql, redis-cli) run on the user app. flynn-host backup / pg:psql
// send X-Flynn-Host-Auth and are allowed.
func refuseDatastoreRunJob(app *ct.App, req *http.Request) error {
	if app == nil || !app.Datastore() {
		return nil
	}
	if hostAuthMatches(req) {
		return nil
	}
	return ct.ValidationError{Field: "app", Message: ct.DatastoreJobExecMessage}
}

func hostAuthMatches(req *http.Request) bool {
	if req == nil {
		return false
	}
	want := os.Getenv("FLYNN_HOST_AUTH_KEY")
	got := req.Header.Get(httphelper.HeaderFlynnHostAuth)
	return secretEqual(want, got)
}

func secretEqual(want, got string) bool {
	if want == "" || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

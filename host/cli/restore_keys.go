package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

// restoreKeySources are the restored cluster's own keys, read from the
// controller release in the backup. A fresh bootstrap gives the blobstore,
// router, logaggregator and tarreceive the same controller key and the same
// ACCESS_TOKEN_KEY (the public half of the access-token keypair) as the
// controller, so a restore fills missing keys from there instead of
// inventing new ones that no other component holds.
type restoreKeySources struct {
	// ClusterKey is the first entry of the controller AUTH_KEY (or
	// CONTROLLER_KEY): the key cluster clients send.
	ClusterKey string
	// AccessTokenKey is the controller ACCESS_TOKEN_KEY, or "" when it is
	// missing or does not parse.
	AccessTokenKey string
}

func restoreKeySourcesFrom(controllerEnv map[string]string) restoreKeySources {
	var src restoreKeySources
	for _, name := range []string{"AUTH_KEY", "CONTROLLER_KEY"} {
		for _, k := range strings.Split(controllerEnv[name], ",") {
			if k = strings.TrimSpace(k); k != "" {
				src.ClusterKey = k
				break
			}
		}
		if src.ClusterKey != "" {
			break
		}
	}
	if tk := strings.TrimSpace(controllerEnv["ACCESS_TOKEN_KEY"]); validTokenKey(tk) {
		src.AccessTokenKey = tk
	}
	return src
}

func validTokenKey(tk string) bool {
	if tk == "" {
		return false
	}
	k, err := authorizer.ParseTokenKey(tk)
	return err == nil && k != nil
}

// restoreKeyField is one env var set to the cluster key when none of the
// accept vars (the ones the app reads a cluster key from) is set.
type restoreKeyField struct {
	set    string
	accept []string
}

type restoreKeySpec struct {
	fields []restoreKeyField
	// tokenKey: ACCESS_TOKEN_KEY is filled, or replaced when it does not
	// parse (the app ignores or exits on such a value, so it carries
	// nothing worth keeping).
	tokenKey bool
	// required: fail the restore when the cluster key cannot be filled
	// (the app would refuse every request).
	required bool
}

// restoreKeySpecs are the system apps that a restore starts from their
// backed-up releases and that refuse requests or exit without these keys.
var restoreKeySpecs = map[string]restoreKeySpec{
	// EDGE-12: no AUTH_KEY/CONTROLLER_KEY and no ACCESS_TOKEN_KEY means
	// every object request is refused.
	"blobstore": {
		fields:   []restoreKeyField{{"AUTH_KEY", []string{"AUTH_KEY", "CONTROLLER_KEY"}}},
		tokenKey: true,
		required: true,
	},
	// EDGE-04: the router and logaggregator APIs take the cluster key and
	// refuse everything without one.
	"router":        {fields: []restoreKeyField{{"CONTROLLER_KEY", []string{"CONTROLLER_KEY", "AUTH_KEY"}}}},
	"logaggregator": {fields: []restoreKeyField{{"CONTROLLER_KEY", []string{"CONTROLLER_KEY", "AUTH_KEY"}}}},
	// tarreceive exits without CONTROLLER_KEY or AUTH_KEY and on an
	// ACCESS_TOKEN_KEY that does not parse.
	"tarreceive": {
		fields: []restoreKeyField{
			{"CONTROLLER_KEY", []string{"CONTROLLER_KEY"}},
			{"AUTH_KEY", []string{"AUTH_KEY"}},
		},
		tokenKey: true,
	},
}

// restoreKeyApps is the order the restore fills keys in, before the
// blobstore run-app step and before the scheduler starts the others.
var restoreKeyApps = []string{"blobstore", "router", "logaggregator", "tarreceive"}

type restoreKeyResult struct {
	// Filled names the env vars that were empty and are now set.
	Filled []string
	// Replaced names present values that did not parse and were replaced.
	Replaced []string
	// Warnings name keys only, never values.
	Warnings []string
}

func (r restoreKeyResult) changed() bool { return len(r.Filled)+len(r.Replaced) > 0 }

// fillRestoredKeys returns a copy of a restored release env with missing
// keys filled from src. A present key is never overwritten, except an
// ACCESS_TOKEN_KEY that does not parse, which is replaced with a valid
// controller key and reported. env is not modified.
func fillRestoredKeys(app string, env map[string]string, src restoreKeySources) (map[string]string, restoreKeyResult, error) {
	out := make(map[string]string, len(env)+3)
	for k, v := range env {
		out[k] = v
	}
	var res restoreKeyResult
	spec, ok := restoreKeySpecs[app]
	if !ok {
		return out, res, nil
	}
	for _, f := range spec.fields {
		if anySet(out, f.accept) {
			continue
		}
		if src.ClusterKey == "" {
			if spec.required {
				return nil, res, fmt.Errorf("restore: the %s release in the backup has no %s, and the backup's controller release has no AUTH_KEY or CONTROLLER_KEY to copy into it; the %s would refuse every request, so the restore stops before starting it. Set AUTH_KEY on the controller release of the source cluster, take a new backup and restore that", app, strings.Join(f.accept, " or "), app)
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s release has no %s and the backup's controller release has no cluster key to copy; set %s on the %s release after the restore", app, strings.Join(f.accept, " or "), f.set, app))
			continue
		}
		out[f.set] = src.ClusterKey
		res.Filled = append(res.Filled, f.set)
	}
	if spec.tokenKey {
		present := strings.TrimSpace(out["ACCESS_TOKEN_KEY"])
		switch {
		case present != "" && validTokenKey(present):
		case present != "" && src.AccessTokenKey != "":
			out["ACCESS_TOKEN_KEY"] = src.AccessTokenKey
			res.Replaced = append(res.Replaced, "ACCESS_TOKEN_KEY")
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s ACCESS_TOKEN_KEY in the backup is not a valid token key (the %s ignores or rejects it); replaced it with the controller's ACCESS_TOKEN_KEY", app, app))
		case present != "":
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s ACCESS_TOKEN_KEY in the backup is not a valid token key and the controller release has no valid one to copy; kept as it is. Scoped build tokens are refused until it is fixed: run 'flynn-host update' (it repairs the access-token keypair) or set a valid ACCESS_TOKEN_KEY on the %s release", app, app))
		case src.AccessTokenKey != "":
			out["ACCESS_TOKEN_KEY"] = src.AccessTokenKey
			res.Filled = append(res.Filled, "ACCESS_TOKEN_KEY")
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s release has no ACCESS_TOKEN_KEY and the controller release has no valid one to copy; scoped build tokens are refused until 'flynn-host update' generates the access-token keypair", app))
		}
	}
	sort.Strings(res.Filled)
	return out, res, nil
}

func anySet(env map[string]string, names []string) bool {
	for _, n := range names {
		if strings.TrimSpace(env[n]) != "" {
			return true
		}
	}
	return false
}

type restoreReleaseClient interface {
	GetApp(string) (*ct.App, error)
	GetAppRelease(string) (*ct.Release, error)
	GetFormation(appID, releaseID string) (*ct.Formation, error)
	CreateRelease(appID string, release *ct.Release) error
	PutFormation(*ct.Formation) error
	SetAppRelease(appID, releaseID string) error
}

// restoreFillAppKeys fills missing keys on a restored system app as a new
// release: the formation moves to it, the old formation is scaled to zero
// (so the scheduler does not also start the old release), and the app
// points at it. An app that is not in the backup is skipped. Only key names
// are written to out.
func restoreFillAppKeys(client restoreReleaseClient, name string, src restoreKeySources, out io.Writer) error {
	app, err := client.GetApp(name)
	if err == controller.ErrNotFound {
		return nil
	} else if err != nil {
		return fmt.Errorf("restore: error getting %s app: %s", name, err)
	}
	rel, err := client.GetAppRelease(app.ID)
	if err != nil {
		return fmt.Errorf("restore: error getting %s release: %s", name, err)
	}
	env, res, err := fillRestoredKeys(name, rel.Env, src)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "WARN: %s\n", w)
	}
	if !res.changed() {
		return nil
	}
	formation, err := client.GetFormation(app.ID, rel.ID)
	if err != nil && err != controller.ErrNotFound {
		return fmt.Errorf("restore: error getting %s formation: %s", name, err)
	}
	next := *rel
	next.ID = ""
	next.CreatedAt = nil
	next.Env = env
	if err := client.CreateRelease(app.ID, &next); err != nil {
		return fmt.Errorf("restore: error creating %s release with filled keys: %s", name, err)
	}
	if formation != nil {
		procs := make(map[string]int, len(formation.Processes))
		zero := make(map[string]int, len(formation.Processes))
		for typ, n := range formation.Processes {
			procs[typ] = n
			zero[typ] = 0
		}
		if err := client.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: next.ID, Processes: procs, Tags: formation.Tags}); err != nil {
			return fmt.Errorf("restore: error moving %s formation to the new release: %s", name, err)
		}
		if err := client.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: rel.ID, Processes: zero, Tags: formation.Tags}); err != nil {
			return fmt.Errorf("restore: error scaling down the old %s release: %s", name, err)
		}
	}
	if err := client.SetAppRelease(app.ID, next.ID); err != nil {
		return fmt.Errorf("restore: error setting %s release: %s", name, err)
	}
	var what []string
	if len(res.Filled) > 0 {
		what = append(what, "filled "+strings.Join(res.Filled, ", "))
	}
	if len(res.Replaced) > 0 {
		what = append(what, "replaced invalid "+strings.Join(res.Replaced, ", "))
	}
	fmt.Fprintf(out, "restore: %s: %s from the restored controller release (new release %s; values not shown)\n", name, strings.Join(what, "; "), next.ID)
	return nil
}

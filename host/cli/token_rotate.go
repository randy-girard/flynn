package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/servicetoken"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	Register("token:rotate", runTokenRotate, `
usage: flynn-host token:rotate <app> <env> <scope> [--caller=<name>]

Mint a new account:quota or acme:issue token and deploy it on <app> as <env>.

The cluster key and the signing key stay on the controller release. They are
not printed and they are not copied onto <app>. account:quota refreshes
BILLING_AUTH_HASH and acme:issue refreshes ACME_AUTH_HASH from the controller
key so those APIs can still recognize that key.
`)
}

func runTokenRotate(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	caller := args.String["--caller"]
	return rotateAppToken(client, args.String["<app>"], args.String["<env>"], args.String["<scope>"], caller)
}

type tokenRotateClient interface {
	GetApp(name string) (*ct.App, error)
	GetAppRelease(appID string) (*ct.Release, error)
	CreateRelease(appID string, release *ct.Release) error
	DeployAppRelease(appID, releaseID string, stopWait <-chan struct{}) error
}

func rotateAppToken(client tokenRotateClient, appName, envName, scope, caller string) error {
	ctrl, err := client.GetApp("controller")
	if err != nil {
		return err
	}
	ctrlRel, err := client.GetAppRelease(ctrl.ID)
	if err != nil {
		return err
	}
	rawKey := ""
	if ctrlRel != nil && ctrlRel.Env != nil {
		rawKey = ctrlRel.Env["ACCESS_TOKEN_SIGNING_KEY"]
	}
	signer, err := tokensigner.ParseSigningKey(rawKey)
	if err != nil {
		return fmt.Errorf("controller signing key: %w", err)
	}
	token, err := servicetoken.Mint(signer, scope, caller, servicetoken.DefaultTTL)
	if err != nil {
		return err
	}
	clusterKey := ""
	if ctrlRel.Env != nil {
		clusterKey = ctrlRel.Env["CONTROLLER_KEY"]
	}
	app, err := client.GetApp(appName)
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease(app.ID)
	if err != nil {
		return err
	}
	if release.Env == nil {
		release.Env = map[string]string{}
	}
	rotateServiceEnv(release.Env, envName, token, scope, clusterKey)
	release.ID = ""
	if err := client.CreateRelease(app.ID, release); err != nil {
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return err
	}
	fmt.Printf("rotated %s on %s\n", envName, app.Name)
	return nil
}

// rotateServiceEnv writes the new token and removes cluster-root secrets.
// clusterKey is used only to refresh BILLING_AUTH_HASH. It is not stored.
func rotateServiceEnv(env map[string]string, name, token, scope, clusterKey string) {
	if env == nil {
		return
	}
	env[name] = token
	switch scope {
	case "account:quota", "acme:issue":
		for _, k := range []string{"CONTROLLER_KEY", "AUTH_KEY", "ACCESS_TOKEN_SIGNING_KEY", "ACCESS_TOKEN_PRIVATE_KEY"} {
			delete(env, k)
		}
	}
	if clusterKey == "" {
		return
	}
	sum := sha256.Sum256([]byte(clusterKey))
	hash := hex.EncodeToString(sum[:])
	switch scope {
	case "account:quota":
		env["BILLING_AUTH_HASH"] = hash
	case "acme:issue":
		env["ACME_AUTH_HASH"] = hash
	}
}

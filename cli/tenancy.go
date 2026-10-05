package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	v1 "github.com/randy-girard/flynn/controller/client/v1"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("whoami", runWhoAmI, `
usage: flynn whoami

Print the authenticated user.
`)
	register("token", runTokenList, `
usage: flynn token
       flynn token:list

List personal access tokens. Secrets are not shown. token:list is the same
command. token:help is the same as token --help.
`)
	register("token:create", runTokenCreate, `
usage: flynn token:create [--scope <scope>] [--expires <time>] [<name>]

Mint a personal access token. The token is printed once.

Options:
	--scope=<scope>    Space-separated scopes stored on the token
	--expires=<time>   RFC3339 expiry

Examples:

	$ flynn token:create laptop
	$ flynn token:create --scope "app:read app:deploy" ci
`)
	register("token:list", runTokenList, `
usage: flynn token:list

List personal access tokens. Secrets are not shown.
`)
	register("token:revoke", runTokenRevoke, `
usage: flynn token:revoke <id>

Revoke a personal access token by id.
`)
	register("context", runContextShow, `
usage: flynn context

Print the current context handle for this cluster. Context chooses the default
owner for create and collaborator commands. It is not an access check.
`)
	register("context:list", runContextList, `
usage: flynn context:list

List the stored context handle for each cluster in ~/.flynnrc.
`)
	register("context:use", runContextUse, `
usage: flynn context:use <handle>

Remember <handle> as the context for the current cluster.

Examples:

	$ flynn context:use ada
`)
	register("apps:transfer", runAppsTransfer, `
usage: flynn apps:transfer <app> <handle>

Transfer <app> to the account named by <handle>. Requires admin on both sides.
`)
	register("collaborator", runCollabList, `
usage: flynn collaborator
       flynn collaborator:list

List collaborators on the current context account, or on -a <app>.
collaborator:list is the same command. collaborator:help is the same as
collaborator --help.
`)
	register("collaborator:list", runCollabList, `
usage: flynn collaborator:list

List collaborators on the current context account, or on -a <app>.
`)
	register("collaborator:add", runCollabAdd, `
usage: flynn collaborator:add [--role <role>] [--password <password>] <email>

Add a collaborator by email. Roles: view, deploy, manage, admin. If the email
does not exist, a non-admin user is created (optional --password).

Options:
	--role=<role>          view, deploy, manage, or admin [default: view]
	--password=<password>  password for a newly created user
`)
	register("collaborator:remove", runCollabRemove, `
usage: flynn collaborator:remove <email>

Remove a collaborator from the current context account, or from -a <app>.
`)
	register("account:suspend", runAccountSuspend(true), `
usage: flynn account:suspend <handle>

Suspend an account so it cannot create or run apps.
`)
	register("account:unsuspend", runAccountSuspend(false), `
usage: flynn account:unsuspend <handle>

Unsuspend an account.
`)
	register("account:quota:set", runQuotaSet, `
usage: flynn account:quota:set [--apps=<apps>] [--processes=<processes>] [--memory=<mb>] [--resources=<resources>] [--collaborators=<collaborators>] <handle>

Set explicit quota limits. Omit a flag to leave that limit unlimited. Zero and
negative values are rejected by the controller.

Options:
	--apps=<apps>                    max apps
	--processes=<processes>          max processes
	--memory=<mb>                    max memory in MB
	--resources=<resources>          max resources
	--collaborators=<collaborators>  max collaborators

Examples:

	$ flynn account:quota:set --apps=5 ada
`)
}

func apiV1(c controller.Client) (*v1.Client, error) {
	v, ok := c.(*v1.Client)
	if !ok || v == nil {
		return nil, fmt.Errorf("controller client cannot call the tenancy API")
	}
	return v, nil
}

func resolveOwner(c controller.Client, handleOrAccount string) (string, error) {
	handleOrAccount = strings.TrimSpace(handleOrAccount)
	if strings.Contains(handleOrAccount, ":") {
		return handleOrAccount, nil
	}
	if strings.Contains(handleOrAccount, "@") {
		u, err := userByEmail(c, handleOrAccount)
		if err != nil {
			return "", err
		}
		return "user:" + u.ID, nil
	}
	v, err := apiV1(c)
	if err != nil {
		return "", err
	}
	var h ct.Handle
	if err := v.Get("/handles/"+url.PathEscape(handleOrAccount), &h); err != nil {
		return "", err
	}
	return h.Account, nil
}

func currentAccount(c controller.Client) (string, error) {
	if clusterConf != nil && strings.TrimSpace(clusterConf.Context) != "" {
		return resolveOwner(c, clusterConf.Context)
	}
	v, err := apiV1(c)
	if err != nil {
		return "", err
	}
	var me ct.WhoAmI
	if err := v.Get("/whoami", &me); err != nil {
		return "", err
	}
	if me.Account == "" {
		return "", fmt.Errorf("no context is set and this credential has no personal account")
	}
	return me.Account, nil
}

func runWhoAmI(_ *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	var me ct.WhoAmI
	if err := v.Get("/whoami", &me); err != nil {
		return err
	}
	fmt.Printf("email: %s\naccount: %s\ncluster_admin: %v\n", me.Email, me.Account, me.ClusterAdmin)
	return nil
}

func runTokenCreate(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	body := ct.TokenCreate{Name: args.String["<name>"], Scopes: args.String["--scope"]}
	if exp := strings.TrimSpace(args.String["--expires"]); exp != "" {
		t, err := time.Parse(time.RFC3339, exp)
		if err != nil {
			return fmt.Errorf("--expires must be RFC3339: %w", err)
		}
		body.ExpiresAt = &t
	}
	var rec ct.AccessTokenRecord
	if err := v.Post("/tokens", body, &rec); err != nil {
		return err
	}
	fmt.Println(rec.Token)
	return nil
}

func runTokenList(_ *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	var recs []ct.AccessTokenRecord
	if err := v.Get("/tokens", &recs); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSCOPES")
	for _, rec := range recs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", rec.ID, rec.Name, rec.Scopes)
	}
	return w.Flush()
}

func runTokenRevoke(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	return v.Delete("/tokens/"+url.PathEscape(args.String["<id>"]), nil)
}

func runContextShow(_ *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	if clusterConf == nil || clusterConf.Context == "" {
		fmt.Println("(none)")
		return nil
	}
	fmt.Println(clusterConf.Context)
	return nil
}

func runContextList(_ *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	for _, s := range config.Clusters {
		ctx := s.Context
		if ctx == "" {
			ctx = "(none)"
		}
		mark := ""
		if s.Name == config.Default {
			mark = " (default cluster)"
		}
		fmt.Printf("%s\t%s%s\n", s.Name, ctx, mark)
	}
	return nil
}

func runContextUse(args *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	cluster, err := getCluster()
	if err != nil {
		return err
	}
	cluster.Context = strings.TrimSpace(args.String["<handle>"])
	return config.SaveTo(configPath())
}

func runAppsTransfer(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	account, err := resolveOwner(c, args.String["<handle>"])
	if err != nil {
		return err
	}
	var app ct.App
	return v.Post("/apps/"+url.PathEscape(args.String["<app>"])+"/transfer", ct.TransferRequest{OwnerAccount: account}, &app)
}

func collabBase(c controller.Client) (string, error) {
	v, err := apiV1(c)
	if err != nil {
		return "", err
	}
	if app := appFromFlag(); app != "" {
		return "/apps/" + url.PathEscape(app) + "/collaborators", nil
	}
	account, err := currentAccount(c)
	if err != nil {
		return "", err
	}
	_ = v
	return "/accounts/" + url.PathEscape(account) + "/collaborators", nil
}

func appFromFlag() string {
	if flagApp != "" {
		return flagApp
	}
	return ""
}

func runCollabList(_ *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	base, err := collabBase(c)
	if err != nil {
		return err
	}
	var rows []ct.Collaborator
	if err := v.Get(base, &rows); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "EMAIL\tUSER\tROLE")
	for _, row := range rows {
		label := row.Email
		if label == "" {
			label = row.Handle
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", label, row.UserID, row.Role)
	}
	return w.Flush()
}

func runCollabAdd(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(firstNonEmpty(args.String["<email>"], args.String["--email"])))
	if !strings.Contains(email, "@") {
		return fmt.Errorf("email is required")
	}
	if _, err := userByEmail(c, email); err != nil {
		password := args.String["--password"]
		generated := false
		if password == "" {
			buf := make([]byte, 12)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			password = hex.EncodeToString(buf)
			generated = true
		}
		body := ct.UserCreate{
			Email:    email,
			Password: password,
		}
		if err := v.Post("/users", body, &ct.User{}); err != nil {
			return fmt.Errorf("create user %s: %w", email, err)
		}
		if generated {
			fmt.Printf("created user %s password: %s\n", email, password)
		}
	}
	base, err := collabBase(c)
	if err != nil {
		return err
	}
	body := ct.CollaboratorCreate{Email: email, Role: args.String["--role"]}
	if body.Role == "" {
		body.Role = "view"
	}
	var row ct.Collaborator
	return v.Post(base, body, &row)
}

func runCollabRemove(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	base, err := collabBase(c)
	if err != nil {
		return err
	}
	accountUser, err := resolveOwner(c, firstNonEmpty(args.String["<email>"], args.String["<handle>"]))
	if err != nil {
		return err
	}
	id := strings.TrimPrefix(accountUser, "user:")
	return v.Delete(base+"/"+url.PathEscape(id), nil)
}

func userByEmail(c controller.Client, email string) (*ct.User, error) {
	v, err := apiV1(c)
	if err != nil {
		return nil, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	var users []*ct.User
	if err := v.Get("/users", &users); err != nil {
		return nil, err
	}
	for _, u := range users {
		if u != nil && strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("user %s not found", email)
}

func runAccountSuspend(suspend bool) func(*docopt.Args, controller.Client) error {
	return func(args *docopt.Args, c controller.Client) error {
		account, err := resolveOwner(c, args.String["<handle>"])
		if err != nil {
			return err
		}
		v, err := apiV1(c)
		if err != nil {
			return err
		}
		action := "unsuspend"
		if suspend {
			action = "suspend"
		}
		return v.Post("/accounts/"+url.PathEscape(account)+"/"+action, struct{}{}, nil)
	}
}

func runQuotaSet(args *docopt.Args, c controller.Client) error {
	account, err := resolveOwner(c, args.String["<handle>"])
	if err != nil {
		return err
	}
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	body := ct.AccountQuota{Account: account}
	if s := args.String["--apps"]; s != "" {
		n, err := parsePos(s)
		if err != nil {
			return err
		}
		body.MaxApps = &n
	}
	if s := args.String["--processes"]; s != "" {
		n, err := parsePos(s)
		if err != nil {
			return err
		}
		body.MaxProcesses = &n
	}
	if s := args.String["--memory"]; s != "" {
		n, err := parsePos(s)
		if err != nil {
			return err
		}
		body.MaxMemoryMB = &n
	}
	if s := args.String["--resources"]; s != "" {
		n, err := parsePos(s)
		if err != nil {
			return err
		}
		body.MaxResources = &n
	}
	if s := args.String["--collaborators"]; s != "" {
		n, err := parsePos(s)
		if err != nil {
			return err
		}
		body.MaxCollaborators = &n
	}
	return v.Put("/accounts/"+url.PathEscape(account)+"/quota", body, &ct.AccountQuota{})
}

func parsePos(s string) (int, error) {
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("quota values must be positive integers")
	}
	return n, nil
}

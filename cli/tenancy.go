package main

import (
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

Print the authenticated user. The cluster key prints as the operator.
`)
	register("token", func(_ *docopt.Args) error { fmt.Print(formatHelp("token")); return nil }, `
usage: flynn token

Create, list, and revoke personal access tokens.
`)
	register("token:create", runTokenCreate, `
usage: flynn token:create [--scope <scope>] [--expires <time>] [<name>]

Mint a personal access token. The token is printed once.

Options:
	--scope=<scope>    Space-separated scopes stored on the token
	--expires=<time>   RFC3339 expiry
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
`)
	register("apps:transfer", runAppsTransfer, `
usage: flynn apps:transfer <app> <handle>

Transfer <app> to the account named by <handle>. Requires admin on both sides.
`)
	register("collaborator", func(_ *docopt.Args) error { fmt.Print(formatHelp("collaborator")); return nil }, `
usage: flynn collaborator

Manage account collaborators, or app collaborators when -a is set.
`)
	register("collaborator:list", runCollabList, `
usage: flynn collaborator:list

List collaborators on the current context account, or on -a <app>.
`)
	register("collaborator:add", runCollabAdd, `
usage: flynn collaborator:add [--role <role>] <handle>

Add a collaborator. Roles: view, deploy, manage, admin.

Options:
	--role=<role>  view, deploy, manage, or admin [default: view]
`)
	register("collaborator:remove", runCollabRemove, `
usage: flynn collaborator:remove <handle>
`)
	register("user", func(_ *docopt.Args) error { fmt.Print(formatHelp("user")); return nil }, `
usage: flynn user

Operator commands for controller users. Requires the cluster key or a cluster admin.
`)
	register("user:list", runUserList, `usage: flynn user:list`)
	register("user:info", runUserInfo, `usage: flynn user:info <handle>`)
	register("user:create", runUserCreate, `
usage: flynn user:create [--handle <handle>] [--password <password>] [--admin] <email>

Create a user and print nothing about the password except what you passed in.
`)
	register("user:disable", runUserFlag("disabled", true), `usage: flynn user:disable <handle>`)
	register("user:enable", runUserFlag("disabled", false), `usage: flynn user:enable <handle>`)
	register("user:admin", runUserAdmin, `usage: flynn user:admin <handle>`)
	register("user:token", runUserToken, `
usage: flynn user:token <handle>

Mint a personal access token for <handle> and print it once.
`)
	register("account:suspend", runAccountSuspend(true), `usage: flynn account:suspend <handle>`)
	register("account:unsuspend", runAccountSuspend(false), `usage: flynn account:unsuspend <handle>`)
	register("account:quota:set", runQuotaSet, `
usage: flynn account:quota:set [--apps=<apps>] [--processes=<processes>] [--memory=<mb>] [--resources=<resources>] [--collaborators=<collaborators>] <handle>

Set explicit quota limits. Omit a flag to leave that limit unlimited. Zero and
negative values are rejected by the controller.
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
	fmt.Printf("handle: %s\nemail: %s\naccount: %s\ncluster_admin: %v\n", me.Handle, me.Email, me.Account, me.ClusterAdmin)
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
	fmt.Fprintln(w, "HANDLE\tUSER\tROLE")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", row.Handle, row.UserID, row.Role)
	}
	return w.Flush()
}

func runCollabAdd(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	base, err := collabBase(c)
	if err != nil {
		return err
	}
	body := ct.CollaboratorCreate{Handle: args.String["<handle>"], Role: args.String["--role"]}
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
	accountUser, err := resolveOwner(c, args.String["<handle>"])
	if err != nil {
		return err
	}
	id := strings.TrimPrefix(accountUser, "user:")
	return v.Delete(base+"/"+url.PathEscape(id), nil)
}

func runUserList(_ *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	var users []*ct.User
	if err := v.Get("/users", &users); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tEMAIL\tADMIN\tDISABLED\tSUSPENDED")
	for _, u := range users {
		fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%v\n", u.Handle, u.Email, u.ClusterAdmin, u.Disabled, u.Suspended)
	}
	return w.Flush()
}

func userByHandle(c controller.Client, handle string) (*ct.User, error) {
	v, err := apiV1(c)
	if err != nil {
		return nil, err
	}
	var h ct.Handle
	if err := v.Get("/handles/"+url.PathEscape(handle), &h); err != nil {
		return nil, err
	}
	id := strings.TrimPrefix(h.Account, "user:")
	var u ct.User
	if err := v.Get("/users/"+url.PathEscape(id), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func runUserInfo(args *docopt.Args, c controller.Client) error {
	u, err := userByHandle(c, args.String["<handle>"])
	if err != nil {
		return err
	}
	fmt.Printf("id: %s\nhandle: %s\nemail: %s\ncluster_admin: %v\ndisabled: %v\nsuspended: %v\n", u.ID, u.Handle, u.Email, u.ClusterAdmin, u.Disabled, u.Suspended)
	return nil
}

func runUserCreate(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	body := ct.UserCreate{
		Email:        args.String["<email>"],
		Handle:       args.String["--handle"],
		Password:     args.String["--password"],
		ClusterAdmin: args.Bool["--admin"],
	}
	if body.Handle == "" {
		body.Handle = strings.Split(body.Email, "@")[0]
	}
	var u ct.User
	return v.Post("/users", body, &u)
}

func runUserFlag(field string, value bool) func(*docopt.Args, controller.Client) error {
	return func(args *docopt.Args, c controller.Client) error {
		u, err := userByHandle(c, args.String["<handle>"])
		if err != nil {
			return err
		}
		v, err := apiV1(c)
		if err != nil {
			return err
		}
		body := ct.UserPatch{}
		if field == "disabled" {
			body.Disabled = &value
		}
		return v.Send("PATCH", "/users/"+url.PathEscape(u.ID), body, &ct.User{})
	}
}

func runUserAdmin(args *docopt.Args, c controller.Client) error {
	u, err := userByHandle(c, args.String["<handle>"])
	if err != nil {
		return err
	}
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	yes := true
	return v.Send("PATCH", "/users/"+url.PathEscape(u.ID), ct.UserPatch{ClusterAdmin: &yes}, &ct.User{})
}

func runUserToken(args *docopt.Args, c controller.Client) error {
	u, err := userByHandle(c, args.String["<handle>"])
	if err != nil {
		return err
	}
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	var rec ct.AccessTokenRecord
	if err := v.Post("/users/"+url.PathEscape(u.ID)+"/bootstrap-token", nil, &rec); err != nil {
		return err
	}
	fmt.Println(rec.Token)
	return nil
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

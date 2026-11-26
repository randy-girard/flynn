package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	v1 "github.com/randy-girard/flynn/controller/client/v1"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/networkpolicy"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/term"
)

func init() {
	Register("user", runHostUser, `
usage: flynn-host user
       flynn-host user <command> [<args>...]

Operator user commands on this host using the local cluster key.
user:bootstrap-admin creates or resets a cluster admin. user:create,
user:list, and user:admin add more cluster admins after bootstrap.
`)
	Register("user:list", runHostUserList, `
usage: flynn-host user:list

List controller users.
`)
	Register("user:info", runHostUserInfo, `
usage: flynn-host user:info <email>

Show a controller user by email.
`)
	Register("user:create", runHostUserCreate, `
usage: flynn-host user:create [--password <password>] [--admin] <email>

Create a user. Sign-in is email and password. --admin grants cluster
administrator (this host uses the cluster key, so it is allowed).

Options:
	--password=<password>  password (prompt on a TTY; generate otherwise)
	--admin                grant cluster administrator

Examples:

	$ flynn-host user:create ada@example.com
	$ flynn-host user:create --admin ada@example.com
`)
	Register("user:disable", runHostUserFlag("disabled", true), `
usage: flynn-host user:disable <email>

Disable a controller user so they cannot sign in.
`)
	Register("user:enable", runHostUserFlag("disabled", false), `
usage: flynn-host user:enable <email>

Enable a disabled controller user.
`)
	Register("user:admin", runHostUserAdmin, `
usage: flynn-host user:admin <email>

Grant cluster administrator on a controller user.
`)
	Register("user:token", runHostUserToken, `
usage: flynn-host user:token <email>

Mint a personal access token for <email> and print it once. It expires
after the cluster default lifetime (90 days unless PAT_DEFAULT_LIFETIME is
set on the controller).
`)
	Register("tenancy:mode", runTenancyMode, `
usage: flynn-host tenancy:mode [--force] [<mode>]

Show or set the cluster tenancy mode (self_hosted or hosted) using the local
cluster key. The body is {"mode": mode} only. Hosted does not enable signup
and does not change reserve_resources. hosted is refused when
production:check would fail as hosted: this command runs every row first,
including zfs and overlay on this host, and the controller runs the rows it
can read itself again on the PUT, so a direct PUT /tenancy is refused too.
self_hosted is refused while the cluster is hosted and a non-system app
still has an owner. That reversal is not safe.

Options:
	--force  cluster key only. With hosted: switch even though
	         production:check fails; the failing rows are printed and the
	         controller records tenancy.force_hosted in its audit log.
	         With self_hosted: switch a hosted cluster even though a
	         non-system app still has an owner_account, recorded as
	         tenancy.force_self_hosted. owner_account is not cleared,
	         account_quotas rows stay, an account with no quota row becomes
	         unlimited immediately, and reserve_resources is not cleared.
	         On a cluster already in that mode --force has nothing to
	         override and says so.

Examples:

    $ flynn-host tenancy:mode
    $ flynn-host tenancy:mode hosted
    $ flynn-host tenancy:mode --force self_hosted
    $ flynn-host tenancy:mode --force hosted
`)
	Register("tenancy:network-policy", runTenancyNetworkPolicy, `
usage: flynn-host tenancy:network-policy [--mode <mode>] --owner <account> [--allow <spec>]...

Print nftables rules for a tenant. This is a dry run and does not change job
network namespaces. self_hosted prints nothing. --allow is name,cidr,port
and is treated as owned by --owner.

Examples:

    $ flynn-host tenancy:network-policy --owner user:abc --allow db,10.1.0.9/32,5432
`)
	Register("user:bootstrap-admin", runBootstrapAdmin, `
usage: flynn-host user:bootstrap-admin [--password <password>] <email>

Create or reset a cluster_admin user with the local cluster key. A generated
password is printed once when --password is omitted and stdin is not a TTY.

Options:
	--password=<password>  password (prompt on a TTY; generate otherwise)

Examples:

	$ flynn-host user:bootstrap-admin admin@example.com
	$ flynn-host user:bootstrap-admin --password secret admin@example.com
`)
}

func hostV1() (*v1.Client, error) {
	c, err := controllerClient()
	if err != nil {
		return nil, err
	}
	v, ok := c.(*v1.Client)
	if !ok || v == nil {
		return nil, fmt.Errorf("controller client cannot call the tenancy API")
	}
	return v, nil
}

func runHostUser(_ *docopt.Args) error {
	fmt.Print(FormatHelp("user"))
	return nil
}

func runHostUserList(_ *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	var users []*ct.User
	if err := v.Get("/users", &users); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "EMAIL\tADMIN\tDISABLED\tSUSPENDED")
	for _, u := range users {
		fmt.Fprintf(w, "%s\t%v\t%v\t%v\n", u.Email, u.ClusterAdmin, u.Disabled, u.Suspended)
	}
	return w.Flush()
}

func hostUserByEmail(email string) (*ct.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return nil, fmt.Errorf("email is required")
	}
	v, err := hostV1()
	if err != nil {
		return nil, err
	}
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

func runHostUserInfo(args *docopt.Args) error {
	u, err := hostUserByEmail(args.String["<email>"])
	if err != nil {
		return err
	}
	fmt.Printf("id: %s\nemail: %s\ncluster_admin: %v\ndisabled: %v\nsuspended: %v\n", u.ID, u.Email, u.ClusterAdmin, u.Disabled, u.Suspended)
	return nil
}

func hostReadPassword(flag string) (string, bool, error) {
	password := flag
	generated := false
	if password == "" {
		if term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprint(os.Stderr, "Password: ")
			fmt.Scanln(&password)
		} else {
			buf := make([]byte, 12)
			if _, err := rand.Read(buf); err != nil {
				return "", false, err
			}
			password = hex.EncodeToString(buf)
			generated = true
		}
	}
	return password, generated, nil
}

func runHostUserCreate(args *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	email := strings.TrimSpace(strings.ToLower(args.String["<email>"]))
	password, generated, err := hostReadPassword(args.String["--password"])
	if err != nil {
		return err
	}
	body := ct.UserCreate{
		Email:        email,
		Password:     password,
		ClusterAdmin: args.Bool["--admin"],
	}
	var u ct.User
	if err := v.Post("/users", body, &u); err != nil {
		return err
	}
	if generated {
		fmt.Printf("password: %s\n", password)
	}
	fmt.Printf("created user %s admin=%v\n", u.Email, u.ClusterAdmin)
	return nil
}

func runHostUserFlag(field string, value bool) func(*docopt.Args) error {
	return func(args *docopt.Args) error {
		u, err := hostUserByEmail(args.String["<email>"])
		if err != nil {
			return err
		}
		v, err := hostV1()
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

func runHostUserAdmin(args *docopt.Args) error {
	u, err := hostUserByEmail(args.String["<email>"])
	if err != nil {
		return err
	}
	v, err := hostV1()
	if err != nil {
		return err
	}
	yes := true
	return v.Send("PATCH", "/users/"+url.PathEscape(u.ID), ct.UserPatch{ClusterAdmin: &yes}, &ct.User{})
}

func runHostUserToken(args *docopt.Args) error {
	u, err := hostUserByEmail(args.String["<email>"])
	if err != nil {
		return err
	}
	v, err := hostV1()
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

// tenancyModeBody is the only JSON tenancy:mode and plugin:install hosted send.
// signup_enabled is not included.
func tenancyModeBody(mode string) map[string]string {
	return map[string]string{"mode": mode}
}

func runTenancyMode(args *docopt.Args, client *cluster.Client) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	mode := strings.TrimSpace(args.String["<mode>"])
	force := args.Bool["--force"]
	if mode == "" {
		if force {
			return fmt.Errorf("--force needs a mode: flynn-host tenancy:mode --force self_hosted or --force hosted")
		}
		var s ct.TenancySettings
		if err := v.Get("/tenancy", &s); err != nil {
			return err
		}
		fmt.Printf("mode: %s\nsignup_enabled: %v\n", s.Mode, s.SignupEnabled)
		return nil
	}
	hostedCheck := func() error {
		// Same rows as production:check, judged as hosted. This does not
		// print that command or change its lines.
		return hostedProductionBlock(hostProductionCheckConfig(client, "flynn-default"))
	}
	put := func(mode string, force bool) error {
		if !force {
			return v.Put("/tenancy", tenancyModeBody(mode), &ct.TenancySettings{})
		}
		_, err := v.SetTenancyMode(mode, true)
		return err
	}
	current := func() (string, error) {
		var s ct.TenancySettings
		if err := v.Get("/tenancy", &s); err != nil {
			return "", err
		}
		return s.Mode, nil
	}
	return applyTenancyMode(os.Stderr, put, hostedCheck, current, mode, force)
}

// forceSelfHostedWarning is printed before tenancy:mode --force self_hosted
// sends the override to a hosted cluster.
const forceSelfHostedWarning = `WARNING: forcing tenancy_mode self_hosted while the cluster is hosted.
  - owner_account is not cleared on any app.
  - account_quotas rows stay and still apply.
  - an account with no quota row becomes unlimited immediately, including
    while its apps keep running.
  - reserve_resources is not cleared and running jobs are not rescheduled.
  - the controller records this as tenancy.force_self_hosted in core_audit.
`

// forceHostedWarning is printed before tenancy:mode --force hosted sends the
// override past a failing production:check.
const forceHostedWarning = `WARNING: forcing tenancy_mode hosted although production:check fails:
  %s
  - account quotas and hosted reservations apply at once.
  - every failing row stays failing until you fix it (flynn-host production:check).
  - the controller records this as tenancy.force_hosted in core_audit.
`

// applyTenancyMode validates and sends a tenancy mode change. hosted runs
// production:check first, as the controller does again on the PUT; a
// failing row refuses it unless --force, the cluster key's override. --force
// self_hosted warns only when the cluster is hosted. The controller decides
// whether an override is needed and refuses it from anything but the
// cluster key.
func applyTenancyMode(out io.Writer, put func(mode string, force bool) error, hostedCheck func() error, current func() (string, error), mode string, force bool) error {
	if err := tenancy.ValidateMode(mode); err != nil {
		return err
	}
	if mode == tenancy.ModeHosted {
		if err := hostedCheck(); err != nil {
			if !force {
				return fmt.Errorf("%v (the cluster key can override with flynn-host tenancy:mode --force hosted)", err)
			}
			fmt.Fprintf(out, forceHostedWarning, err)
		} else if force {
			fmt.Fprintln(out, "production:check passes; --force has nothing to override.")
		}
	}
	if force && mode == tenancy.ModeSelfHosted {
		cur, err := current()
		if err == nil && cur != tenancy.ModeHosted {
			fmt.Fprintf(out, "tenancy_mode is already %s; --force has nothing to override.\n", cur)
		} else {
			fmt.Fprint(out, forceSelfHostedWarning)
		}
	}
	return put(mode, force)
}

func runTenancyNetworkPolicy(args *docopt.Args) error {
	mode := args.String["--mode"]
	if mode == "" {
		mode = "hosted"
	}
	owner := args.String["--owner"]
	var allowed []networkpolicy.Endpoint
	specs, _ := args.All["--allow"].([]string)
	for _, spec := range specs {
		parts := strings.Split(spec, ",")
		if len(parts) != 3 {
			return fmt.Errorf("--allow must be name,cidr,port")
		}
		var port int
		if _, err := fmt.Sscan(parts[2], &port); err != nil {
			return fmt.Errorf("--allow port: %w", err)
		}
		allowed = append(allowed, networkpolicy.Endpoint{Owner: owner, Name: parts[0], CIDR: parts[1], Port: port})
	}
	text, err := networkpolicy.Rules(mode, owner, networkpolicy.SystemDeny(), allowed)
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func runBootstrapAdmin(args *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	email := strings.TrimSpace(strings.ToLower(args.String["<email>"]))
	password, generated, err := hostReadPassword(args.String["--password"])
	if err != nil {
		return err
	}
	var users []*ct.User
	if err := v.Get("/users", &users); err != nil {
		return err
	}
	var existing *ct.User
	for _, u := range users {
		if u != nil && strings.EqualFold(u.Email, email) {
			existing = u
			break
		}
	}
	if existing != nil {
		yes := true
		body := ct.UserPatch{ClusterAdmin: &yes, Password: &password, Suspended: boolPtr(false), Disabled: boolPtr(false)}
		if err := v.Send("PATCH", "/users/"+url.PathEscape(existing.ID), body, &ct.User{}); err != nil {
			return err
		}
	} else {
		body := ct.UserCreate{Email: email, Password: password, ClusterAdmin: true}
		if err := v.Post("/users", body, &ct.User{}); err != nil {
			return err
		}
	}
	if generated {
		fmt.Printf("password: %s\n", password)
	}
	fmt.Printf("cluster admin %s is ready\n", email)
	return nil
}

func boolPtr(v bool) *bool { return &v }

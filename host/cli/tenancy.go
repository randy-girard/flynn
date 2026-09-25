package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/flynn/go-docopt"
	v1 "github.com/randy-girard/flynn/controller/client/v1"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/networkpolicy"
	"github.com/randy-girard/flynn/pkg/term"
)

func init() {
	Register("tenancy:mode", runTenancyMode, `
usage: flynn-host tenancy:mode [<mode>]

Show or set the cluster tenancy mode (self_hosted or hosted) using the local
cluster key. Hosted mode does not enable public signup.

Examples:

    $ flynn-host tenancy:mode
    $ flynn-host tenancy:mode hosted
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
usage: flynn-host user:bootstrap-admin [--handle <handle>] [--password <password>] <email>

Create or reset a cluster_admin user with the local cluster key. A generated
password is printed once when --password is omitted and stdin is not a TTY.
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

func runTenancyMode(args *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	mode := strings.TrimSpace(args.String["<mode>"])
	if mode == "" {
		var s ct.TenancySettings
		if err := v.Get("/tenancy", &s); err != nil {
			return err
		}
		fmt.Printf("mode: %s\nsignup_enabled: %v\n", s.Mode, s.SignupEnabled)
		return nil
	}
	return v.Put("/tenancy", map[string]interface{}{"mode": mode}, &ct.TenancySettings{})
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
	handle := strings.TrimSpace(strings.ToLower(args.String["--handle"]))
	if handle == "" {
		handle = strings.Split(email, "@")[0]
	}
	password := args.String["--password"]
	generated := false
	if password == "" {
		if term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprint(os.Stderr, "Password: ")
			fmt.Scanln(&password)
		} else {
			buf := make([]byte, 12)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			password = hex.EncodeToString(buf)
			generated = true
		}
	}
	var existing ct.Handle
	err = v.Get("/handles/"+url.PathEscape(handle), &existing)
	if err == nil && strings.HasPrefix(existing.Account, "user:") {
		id := strings.TrimPrefix(existing.Account, "user:")
		yes := true
		body := ct.UserPatch{ClusterAdmin: &yes, Password: &password, Suspended: boolPtr(false), Disabled: boolPtr(false)}
		if err := v.Send("PATCH", "/users/"+url.PathEscape(id), body, &ct.User{}); err != nil {
			return err
		}
	} else {
		body := ct.UserCreate{Email: email, Handle: handle, Password: password, ClusterAdmin: true}
		if err := v.Post("/users", body, &ct.User{}); err != nil {
			return err
		}
	}
	if generated {
		fmt.Printf("password: %s\n", password)
	}
	fmt.Printf("cluster admin %s is ready\n", handle)
	return nil
}

func boolPtr(v bool) *bool { return &v }

package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("domain", runDomainList, `
usage: flynn domain [--owner <handle>] [--all]
       flynn domain:list [--owner <handle>] [--all]

List custom-domain claims of the current context account (hosted mode).
domain:list is the same command. domain:help is the same as domain --help.

Options:
	--owner=<handle>  account whose claims to list (handle, email, or user:/org: id)
	--all             every claim on the cluster (cluster key or cluster admin)
`)
	register("domain:list", runDomainList, `
usage: flynn domain:list [--owner <handle>] [--all]

List custom-domain claims with their status, TXT name and token.

Options:
	--owner=<handle>  account whose claims to list (handle, email, or user:/org: id)
	--all             every claim on the cluster (cluster key or cluster admin)
`)
	register("domain:claim", runDomainClaim, `
usage: flynn domain:claim [--owner <handle>] <hostname>

Claim a custom domain for an account and print the TXT record that proves
it. Publish the record, then run domain:check. A claim holds the name for 7
days; after that another account may claim an unverified name. A verified
name stays with its account until it is removed.

Options:
	--owner=<handle>  account to claim for [default: current context]

Examples:

	$ flynn domain:claim example.com
	$ flynn domain:claim --owner acme shop.example.com
`)
	register("domain:check", runDomainCheck, `
usage: flynn domain:check <hostname>

Look up the TXT record for a claimed domain and mark it verified when it
matches the token.
`)
	register("domain:remove", runDomainRemove, `
usage: flynn domain:remove <hostname>

Delete a custom-domain claim, verified or not. Routes already added under
it stay; new routes need the name claimed and verified again.
`)
}

func runDomainList(args *docopt.Args, c controller.Client) error {
	return domainList(args, c, os.Stdout)
}

func runDomainClaim(args *docopt.Args, c controller.Client) error {
	return domainClaim(args, c, os.Stdout)
}

func runDomainCheck(args *docopt.Args, c controller.Client) error {
	return domainCheck(args, c, os.Stdout)
}

func runDomainRemove(args *docopt.Args, c controller.Client) error {
	return domainRemove(args, c)
}

func domainOwner(args *docopt.Args, c controller.Client) (string, error) {
	if h := strings.TrimSpace(args.String["--owner"]); h != "" {
		return resolveOwner(c, h)
	}
	return currentAccount(c)
}

func domainList(args *docopt.Args, c controller.Client, out io.Writer) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	path := "/domains"
	if !args.Bool["--all"] {
		owner, err := domainOwner(args, c)
		if err != nil {
			return err
		}
		path += "?owner_account=" + url.QueryEscape(owner)
	}
	var list []ct.DomainVerification
	if err := v.Get(path, &list); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "HOSTNAME\tOWNER\tSTATUS\tTXT NAME\tTOKEN")
	for _, d := range list {
		status := "pending"
		if d.VerifiedAt != nil {
			status = "verified"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Hostname, d.OwnerAccount, status, d.TXTName, d.Token)
	}
	return w.Flush()
}

func domainClaim(args *docopt.Args, c controller.Client, out io.Writer) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	owner, err := domainOwner(args, c)
	if err != nil {
		return err
	}
	body := map[string]string{"hostname": args.String["<hostname>"], "owner_account": owner}
	var d ct.DomainVerification
	if err := v.Post("/domains/verify", body, &d); err != nil {
		return err
	}
	fmt.Fprintf(out, "Publish this TXT record, then run: flynn domain:check %s\n\n", d.Hostname)
	fmt.Fprintf(out, "  name:  %s\n  value: %s\n", d.TXTName, d.Token)
	return nil
}

func domainCheck(args *docopt.Args, c controller.Client, out io.Writer) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	host := strings.TrimSpace(args.String["<hostname>"])
	var d ct.DomainVerification
	if err := v.Post("/domains/verify/"+url.PathEscape(host)+"/check", struct{}{}, &d); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is verified\n", d.Hostname)
	return nil
}

func domainRemove(args *docopt.Args, c controller.Client) error {
	v, err := apiV1(c)
	if err != nil {
		return err
	}
	return v.Delete("/domains/"+url.PathEscape(strings.TrimSpace(args.String["<hostname>"])), nil)
}

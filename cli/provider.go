package main

import (
	"log"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("provider", runProviderList, `
usage: flynn provider

List resource providers associated with the controller.
`)
	register("provider:add", runProviderAdd, `
usage: flynn provider:add <name> <url>

Create a new provider <name> at <url>.
`)
}

func runProviderList(args *docopt.Args, client controller.Client) error {
	providers, err := client.ProviderList()
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		return nil
	}

	w := tabWriter()
	defer w.Flush()

	listRec(w, "ID", "NAME", "URL")
	for _, p := range providers {
		listRec(w, p.ID, p.Name, p.URL)
	}

	return nil
}

func runProviderAdd(args *docopt.Args, client controller.Client) error {
	name := args.String["<name>"]
	url := args.String["<url>"]

	if err := client.CreateProvider(&ct.Provider{Name: name, URL: url}); err != nil {
		return err
	}

	log.Printf("Created provider %s.", name)

	return nil
}

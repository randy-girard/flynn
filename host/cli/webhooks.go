package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("webhooks", runWebhooks, `
usage: flynn-host webhooks
       flynn-host webhooks add <url>
       flynn-host webhooks remove <id>

Manage webhook notification endpoints across all hosts.

Commands:
    With no arguments, lists all configured webhooks.

    add       Add a webhook endpoint URL to all hosts
    remove    Remove a webhook by ID from all hosts

Examples:

    $ flynn-host webhooks
    $ flynn-host webhooks add https://example.com/webhook
    $ flynn-host webhooks remove abc-123
`)
}

func runWebhooks(args *docopt.Args, client *cluster.Client) error {
	switch {
	case args.Bool["add"]:
		return runWebhooksAdd(args, client)
	case args.Bool["remove"]:
		return runWebhooksRemove(args, client)
	default:
		return runWebhooksList(client)
	}
}

func runWebhooksList(client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return err
	}

	seen := make(map[string]bool)
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ID", "URL", "CREATED")
	for _, h := range hosts {
		webhooks, err := h.ListWebhooks()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not list webhooks on %s: %s\n", h.ID(), err)
			continue
		}
		for _, wh := range webhooks {
			if seen[wh.ID] {
				continue
			}
			seen[wh.ID] = true
			listRec(w, wh.ID, wh.URL, wh.CreatedAt.Format("2006-01-02 15:04:05"))
		}
	}
	return nil
}

func runWebhooksAdd(args *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return err
	}
	url := args.String["<url>"]
	id := random.UUID()
	var firstErr error
	for _, h := range hosts {
		if _, err := h.AddWebhook(id, url); err != nil {
			fmt.Fprintf(os.Stderr, "error adding webhook on %s: %s\n", h.ID(), err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}
	if firstErr != nil {
		return firstErr
	}
	fmt.Printf("Webhook added: %s\n", id)
	return nil
}

func runWebhooksRemove(args *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return err
	}
	id := args.String["<id>"]
	for _, h := range hosts {
		if err := h.RemoveWebhook(id); err != nil {
			// silently skip hosts that don't have this webhook
			continue
		}
		fmt.Printf("Webhook removed from %s\n", h.ID())
	}
	return nil
}


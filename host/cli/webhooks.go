package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/random"
)

func init() {
	Register("webhooks", runWebhooksListCmd, `
usage: flynn-host webhooks

List webhook notification endpoints across all hosts.

Examples:

    $ flynn-host webhooks
`)
	Register("webhooks:add", runWebhooksAdd, `
usage: flynn-host webhooks:add [-H <header>]... <url>

Add a webhook endpoint URL to all hosts.

Options:
    -H, --header <header>  Header to send on every delivery, "Name: value".
                           May be repeated. Useful for shared-secret auth
                           (e.g. -H "X-Flynn-Webhook-Secret: ...").

Examples:

    $ flynn-host webhooks:add https://example.com/webhook
    $ flynn-host webhooks:add -H "X-Flynn-Webhook-Secret: s3cret" https://example.com/webhook
`)
	Register("webhooks:remove", runWebhooksRemove, `
usage: flynn-host webhooks:remove <id>

Remove a webhook by ID from all hosts.

Examples:

    $ flynn-host webhooks:remove abc-123
`)
}

func runWebhooksListCmd(_ *docopt.Args, client *cluster.Client) error {
	return runWebhooksList(client)
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
	headers, err := parseHeaderFlags(cliutil.List(args, "--header"))
	if err != nil {
		return err
	}
	id := random.UUID()
	var firstErr error
	for _, h := range hosts {
		if _, err := h.AddWebhook(id, url, headers); err != nil {
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

// parseHeaderFlags converts repeated --header flag values of the form
// "Name: value" or "Name=value" into a map suitable for WebhookConfig.Headers.
func parseHeaderFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, h := range values {
		sep := strings.IndexAny(h, ":=")
		if sep <= 0 {
			return nil, fmt.Errorf("invalid header %q: expected \"Name: value\"", h)
		}
		name := strings.TrimSpace(h[:sep])
		val := strings.TrimSpace(h[sep+1:])
		if name == "" {
			return nil, fmt.Errorf("invalid header %q: empty name", h)
		}
		out[name] = val
	}
	return out, nil
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

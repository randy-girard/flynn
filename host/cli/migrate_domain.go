package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("migrate-domain", runMigrateDomain, `
usage: flynn-host migrate-domain <domain>

Migrate the cluster's base domain from the current one to <domain>.

New certificates will be generated for the controller and new routes will be
added with the pattern <app-name>.<domain> for each app.

After the cluster finishes, run 'flynn cluster:refresh' on each laptop to
update ~/.flynnrc, TLS pins, and git remotes.
`)
}

func runMigrateDomain(args *docopt.Args, _ *cluster.Client) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}

	dm := &ct.DomainMigration{
		Domain: args.String["<domain>"],
	}

	release, err := client.GetAppRelease("controller")
	if err != nil {
		return err
	}
	dm.OldDomain = release.Env["DEFAULT_ROUTE_DOMAIN"]

	if !promptYesNo(fmt.Sprintf("Migrate cluster domain from %q to %q?", dm.OldDomain, dm.Domain)) {
		fmt.Println("Aborted")
		return nil
	}

	maxDuration := 2 * time.Minute
	fmt.Printf("Migrating cluster domain (this can take up to %s)...\n", maxDuration)

	events := make(chan *ct.Event)
	stream, err := client.StreamEvents(ct.StreamEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeDomainMigration},
	}, events)
	if err != nil {
		return err
	}
	defer stream.Close()

	if err := client.PutDomain(dm); err != nil {
		return err
	}

	timeout := time.After(maxDuration)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return stream.Err()
			}
			var e *ct.DomainMigrationEvent
			if err := json.Unmarshal(event.Data, &e); err != nil {
				return err
			}
			if e.Error != "" {
				fmt.Println(e.Error)
			}
			if e.DomainMigration.FinishedAt != nil {
				dm = e.DomainMigration
				fmt.Printf("Changed cluster domain from %q to %q\n", dm.OldDomain, dm.Domain)
				fmt.Println("Run 'flynn cluster:refresh' on each laptop to update ~/.flynnrc")
				return nil
			}
		case <-timeout:
			return errors.New("timed out waiting for domain migration to complete")
		}
	}
}

func promptYesNo(msg string) bool {
	fmt.Print(msg)
	fmt.Print(" (yes/no): ")
	for {
		var answer string
		fmt.Scanln(&answer)
		switch answer {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			fmt.Print("Please type 'yes' or 'no': ")
		}
	}
}

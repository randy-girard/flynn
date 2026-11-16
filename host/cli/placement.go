package cli

import (
	"fmt"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("placement:mode", runPlacementMode, `
usage: flynn-host placement:mode [<mode>]

Show or set workload placement (mixed or split) using the local cluster key.
The body is {"mode": mode} only. mixed is the default and ignores flynn-role.
split places new jobs by that tag. It does not stop running jobs and does not
move volumes. split is refused unless a host that is not shutting down has
flynn-role=core. Zero flynn-role=app hosts is allowed.

Examples:

    $ flynn-host placement:mode
    $ flynn-host placement:mode split
    $ flynn-host placement:mode mixed
`)
}

func placementModeBody(mode string) map[string]string {
	return map[string]string{"mode": mode}
}

func runPlacementMode(args *docopt.Args, client *cluster.Client) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	mode := strings.TrimSpace(args.String["<mode>"])
	if mode == "" {
		var s ct.WorkloadPlacement
		if err := v.Get("/cluster/placement", &s); err != nil {
			return err
		}
		fmt.Printf("mode: %s\n", s.Mode)
		return nil
	}
	if err := placement.ValidateMode(mode); err != nil {
		return err
	}
	if err := v.Put("/cluster/placement", placementModeBody(mode), &ct.WorkloadPlacement{}); err != nil {
		return err
	}
	if mode == placement.ModeSplit {
		if msg, err := placementPendingAppHosts(client); err != nil {
			fmt.Printf("warn: could not list hosts: %v\n", err)
		} else if msg != "" {
			fmt.Println(msg)
		}
	}
	return nil
}

// placementPendingAppHosts is the sentence printed after a successful split
// when no host that is not shutting down has flynn-role=app. Existing jobs
// stay where they are.
func placementPendingAppHosts(client *cluster.Client) (string, error) {
	if client == nil {
		return "", nil
	}
	hosts, err := client.Hosts()
	if err != nil {
		return "", err
	}
	return placementPendingMessage(hosts), nil
}

const placementNoAppHostMessage = "new user and tenant jobs stay pending until an app host exists. Existing jobs are left running."

func placementPendingMessage(hosts []*cluster.Host) string {
	if placementHasAppHost(hosts) {
		return ""
	}
	return placementNoAppHostMessage
}

func placementHasAppHost(hosts []*cluster.Host) bool {
	out := make([]placement.Host, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		out = append(out, placement.Host{Tags: h.Tags(), Shutdown: h.ShuttingDown()})
	}
	return placement.HasApp(out)
}

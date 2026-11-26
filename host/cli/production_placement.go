package cli

import (
	"fmt"

	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/productioncheck"
)

func listPlacementJobs(client *cluster.Client) ([]placementJob, error) {
	if client == nil {
		return nil, fmt.Errorf("no cluster client")
	}
	hosts, err := client.Hosts()
	if err != nil {
		return nil, err
	}
	list := make([]productioncheck.JobHost, 0, len(hosts))
	for _, h := range hosts {
		if h != nil {
			list = append(list, h)
		}
	}
	return productioncheck.ListPlacementJobs(list)
}

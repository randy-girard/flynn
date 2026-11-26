package main

import (
	"errors"

	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/productioncheck"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

// hostedProductionCheck is the guard PUT /tenancy runs before a switch to
// hosted: the production:check rows judged as hosted, read through this
// cluster's controller with the cluster key, discoverd's host list and each
// host's running jobs. The zfs and overlay rows read the host the CLI runs
// on, so the controller does not run them; flynn-host tenancy:mode hosted
// still does, before it sends the PUT.
func (c *controllerAPI) hostedProductionCheck() error {
	if len(c.config.keys) == 0 || c.config.keys[0] == "" {
		return errors.New("the controller has no cluster key to read the check with")
	}
	client, err := controller.NewClient("", c.config.keys[0])
	if err != nil {
		return err
	}
	api, ok := client.(productioncheck.API)
	if !ok {
		return errors.New("the controller client cannot read the check")
	}
	return productioncheck.HostedBlock(c.hostedCheckConfig(api))
}

// hostedCheckConfig is the controller's productioncheck.Config: api for the
// controller reads, c.clusterClient for hosts and jobs, and no host-local
// rows.
func (c *controllerAPI) hostedCheckConfig(api productioncheck.API) productioncheck.Config {
	var clients []utils.HostClient
	var hostsErr error
	listed := false
	listHosts := func() ([]utils.HostClient, error) {
		if !listed {
			listed = true
			if c.clusterClient == nil {
				hostsErr = errors.New("no cluster client")
			} else {
				clients, hostsErr = c.clusterClient.Hosts()
			}
		}
		return clients, hostsErr
	}
	cfg := productioncheck.Config{
		API: api,
		Hosts: func() ([]*cluster.Host, error) {
			list, err := listHosts()
			if err != nil {
				return nil, err
			}
			out := make([]*cluster.Host, 0, len(list))
			for _, h := range list {
				if h == nil {
					continue
				}
				if ch, ok := h.(*cluster.Host); ok {
					out = append(out, ch)
					continue
				}
				out = append(out, cluster.NewHost(h.ID(), h.Addr(), nil, h.Tags()))
			}
			return out, nil
		},
		Jobs: func() ([]productioncheck.PlacementJob, error) {
			list, err := listHosts()
			if err != nil {
				return nil, err
			}
			hosts := make([]productioncheck.JobHost, 0, len(list))
			for _, h := range list {
				if h != nil {
					hosts = append(hosts, h)
				}
			}
			return productioncheck.ListPlacementJobs(hosts)
		},
	}
	if cc, ok := api.(controller.Client); ok {
		cfg.SireniaFormations = func() ([]updaterdeploy.SireniaFormationState, error) {
			return updaterdeploy.SireniaFormationStates(cc)
		}
	}
	return cfg
}

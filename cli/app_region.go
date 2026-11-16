package main

import (
	"fmt"
	"net/url"
	"strings"

	cfg "github.com/randy-girard/flynn/cli/config"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

type appCreateCluster struct {
	Name          string
	ControllerURL string
	LoggedIn      bool
}

// chooseAppCreate picks the controller that creates the app. An empty flag
// or this cluster's own region stays here. A linked region uses that
// cluster's ~/.flynnrc login. The app is not labeled on this controller.
func chooseAppCreate(localRegion, flag string, links []ct.RegionLink, clusters []appCreateCluster) (local bool, name string, err error) {
	flag = strings.TrimSpace(flag)
	localRegion = strings.TrimSpace(localRegion)
	if localRegion == "" {
		localRegion = "US"
	}
	if flag == "" || flag == localRegion {
		return true, "", nil
	}
	var link *ct.RegionLink
	for i := range links {
		if links[i].Region == flag {
			link = &links[i]
			break
		}
	}
	if link == nil {
		return false, "", fmt.Errorf("region %s is not this cluster (%s) and is not linked", flag, localRegion)
	}
	var match *appCreateCluster
	for i := range clusters {
		if sameControllerHost(clusters[i].ControllerURL, link.ControllerURL) {
			match = &clusters[i]
			break
		}
	}
	if match == nil {
		return false, "", fmt.Errorf("no ~/.flynnrc entry for region %s (%s): run flynn cluster:add and flynn login", flag, link.Domain)
	}
	if !match.LoggedIn {
		return false, "", fmt.Errorf("no login for region %s (%s): run flynn cluster:add and flynn login", flag, link.Domain)
	}
	return false, match.Name, nil
}

func sameControllerHost(a, b string) bool {
	ha, hb := controllerHost(a), controllerHost(b)
	return ha != "" && strings.EqualFold(ha, hb)
}

func controllerHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func clusterHasLogin(c *cfg.Cluster) bool {
	if c == nil {
		return false
	}
	if cfg.IsPersonalAccessToken(c.Key) {
		return strings.TrimSpace(c.Key) != ""
	}
	_, err := c.Client()
	return err == nil
}

func clientForAppRegion(client controller.Client, flag string) (controller.Client, error) {
	getter, ok := client.(interface {
		Get(path string, out interface{}) error
	})
	if !ok {
		return nil, fmt.Errorf("controller client cannot list regions")
	}
	var list ct.ClusterRegionList
	if err := getter.Get("/cluster/regions", &list); err != nil {
		return nil, err
	}
	if err := readConfig(); err != nil {
		return nil, err
	}
	var clusters []appCreateCluster
	if config != nil {
		for _, c := range config.Clusters {
			if c == nil {
				continue
			}
			clusters = append(clusters, appCreateCluster{
				Name:          c.Name,
				ControllerURL: c.ControllerURL,
				LoggedIn:      clusterHasLogin(c),
			})
		}
	}
	local, name, err := chooseAppCreate(list.Region, flag, list.Links, clusters)
	if err != nil {
		return nil, err
	}
	if local {
		return client, nil
	}
	for _, c := range config.Clusters {
		if c != nil && c.Name == name {
			clusterConf = c
			next, err := c.Client()
			if err != nil {
				return nil, fmt.Errorf("no login for region %s: run flynn cluster:add and flynn login", flag)
			}
			return next, nil
		}
	}
	return nil, fmt.Errorf("no ~/.flynnrc entry for region %s: run flynn cluster:add and flynn login", flag)
}

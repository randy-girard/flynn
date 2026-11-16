package main

import (
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/region"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// fetchPeerRegion dials the peer. Tests replace it. The default sends no
// CONTROLLER_KEY.
var fetchPeerRegion = region.FetchPeerRegion

func (c *controllerAPI) regionName() (string, error) {
	if c == nil || c.tenancy == nil {
		return region.DefaultName, nil
	}
	name, err := c.tenancy.GetRegion()
	if err != nil {
		if err == data.ErrNotFound {
			return region.DefaultName, nil
		}
		return "", err
	}
	if name == "" {
		return region.DefaultName, nil
	}
	return name, nil
}

func (c *controllerAPI) GetClusterRegion(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	name, err := c.regionName()
	if err != nil {
		respondWithError(w, err)
		return
	}
	region.WriteName(w, name)
}

func (c *controllerAPI) PutClusterRegion(_ context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		Region string `json:"region"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	name, err := region.Normalize(body.Region)
	if err != nil {
		respondWithError(w, ct.ValidationError{Field: "region", Message: err.Error()})
		return
	}
	if c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	links, err := c.tenancy.ListRegionLinks()
	if err != nil {
		respondWithError(w, err)
		return
	}
	for _, link := range links {
		if link.Region == name {
			respondWithError(w, ct.ValidationError{Field: "region", Message: "region is already a linked cluster"})
			return
		}
	}
	if err := c.tenancy.SetRegion(name); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ct.ClusterRegionList{Region: name, Links: links})
}

func (c *controllerAPI) GetClusterRegions(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	name, err := c.regionName()
	if err != nil {
		respondWithError(w, err)
		return
	}
	links := []ct.RegionLink{}
	if c != nil && c.tenancy != nil {
		links, err = c.tenancy.ListRegionLinks()
		if err != nil {
			respondWithError(w, err)
			return
		}
	}
	if links == nil {
		links = []ct.RegionLink{}
	}
	httphelper.JSON(w, http.StatusOK, ct.ClusterRegionList{Region: name, Links: links})
}

func (c *controllerAPI) PostClusterRegion(_ context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		Region string `json:"region"`
		Domain string `json:"domain"`
		TLSPin string `json:"tls_pin"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	name, err := region.Normalize(body.Region)
	if err != nil {
		respondWithError(w, ct.ValidationError{Field: "region", Message: err.Error()})
		return
	}
	domain, err := region.ParseDomain(body.Domain)
	if err != nil {
		respondWithError(w, ct.ValidationError{Field: "domain", Message: err.Error()})
		return
	}
	if _, err := region.ParsePin(body.TLSPin); err != nil {
		respondWithError(w, ct.ValidationError{Field: "tls_pin", Message: err.Error()})
		return
	}
	if c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	current, err := c.regionName()
	if err != nil {
		respondWithError(w, err)
		return
	}
	links, err := c.tenancy.ListRegionLinks()
	if err != nil {
		respondWithError(w, err)
		return
	}
	names := make([]string, 0, len(links))
	replacing := false
	for _, link := range links {
		names = append(names, link.Region)
		if link.Region == name {
			replacing = true
		}
	}
	if name == current {
		respondWithError(w, ct.ValidationError{Field: "region", Message: "region is already this cluster"})
		return
	}
	if !replacing && len(links) >= region.MaxLinks {
		respondWithError(w, ct.ValidationError{Field: "region", Message: "at most 64 linked regions"})
		return
	}
	// Dial the derived controller URL only. A client-supplied URL is ignored.
	// This request does not include CONTROLLER_KEY.
	peer, peerErr := fetchPeerRegion("https://controller."+domain, strings.TrimSpace(body.TLSPin))
	link, err := region.PrepareLink(region.LinkInput{
		Region:     name,
		Domain:     domain,
		TLSPin:     body.TLSPin,
		Current:    current,
		Existing:   names,
		PeerRegion: peer,
		PeerErr:    peerErr,
	})
	if err != nil {
		msg := err.Error()
		if err == region.ErrPeerClusterKey {
			msg = "peer offered a cluster key"
		}
		respondWithError(w, ct.ValidationError{Field: "region", Message: msg})
		return
	}
	if err := c.tenancy.UpsertRegionLink(link); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, link)
}

func (c *controllerAPI) DeleteClusterRegion(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	name := strings.TrimSpace(params.ByName("region"))
	if _, err := region.Normalize(name); err != nil {
		respondWithError(w, ct.ValidationError{Field: "region", Message: err.Error()})
		return
	}
	if c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	if err := c.tenancy.DeleteRegionLink(name); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

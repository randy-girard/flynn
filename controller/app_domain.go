package main

import (
	"net/http"
	"os"
	"strings"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func clusterDomainFromEnv() string {
	if d := strings.TrimSpace(os.Getenv("CLUSTER_DOMAIN")); d != "" {
		return d
	}
	return strings.TrimSpace(os.Getenv("DEFAULT_ROUTE_DOMAIN"))
}

func appDomainFallback() string {
	if d := strings.TrimSpace(os.Getenv("DEFAULT_ROUTE_DOMAIN")); d != "" {
		return d
	}
	return strings.TrimSpace(os.Getenv("CLUSTER_DOMAIN"))
}

func (c *controllerAPI) appDomainView() ct.AppDomain {
	override := ""
	if c != nil && c.tenancy != nil {
		if d, err := c.tenancy.GetAppDomain(); err == nil {
			override = d
		}
	}
	fallback := appDomainFallback()
	return ct.AppDomain{
		Domain:        override,
		Effective:     data.EffectiveAppDomain(override, fallback),
		ClusterDomain: clusterDomainFromEnv(),
	}
}

func (c *controllerAPI) GetAppDomain(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	httphelper.JSON(w, http.StatusOK, c.appDomainView())
}

func (c *controllerAPI) PutAppDomain(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	if domain != "" && !data.ValidAppDomain(domain) {
		respondWithError(w, ct.ValidationError{Field: "domain", Message: "must be a hostname such as flynnapps.com"})
		return
	}
	if c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	if err := c.tenancy.SetAppDomain(domain); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, c.appDomainView())
}

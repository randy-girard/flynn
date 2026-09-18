package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	que "github.com/flynn/que-go"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/tlscert"
	"golang.org/x/net/context"
)

func (c *controllerAPI) MigrateDomain(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var dm *ct.DomainMigration
	if err := httphelper.DecodeJSON(req, &dm); err != nil {
		respondWithError(w, err)
		return
	}
	defaultRouteDomain := os.Getenv("DEFAULT_ROUTE_DOMAIN")
	if dm.OldDomain != defaultRouteDomain {
		respondWithError(w, ct.ValidationError{
			Message: fmt.Sprintf(`Can't migrate from "%s" when currently using "%s"`, dm.OldDomain, defaultRouteDomain),
		})
		return
	}

	app, err := c.appRepo.Get("router")
	if err != nil {
		respondWithError(w, err)
		return
	}
	release, err := c.appRepo.GetRelease(app.(*ct.App).ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	dm.OldTLSCert = &tlscert.Cert{
		Cert:       release.Env["TLSCERT"],
		PrivateKey: release.Env["TLSKEY"],
	}

	if err := c.domainMigrationRepo.Add(dm); err != nil {
		respondWithError(w, err)
		return
	}

	args, err := json.Marshal(dm)
	if err != nil {
		respondWithError(w, err)
		return
	}

	if err := c.que.Enqueue(&que.Job{
		Type: "domain_migration",
		Args: args,
	}); err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, &dm)
}

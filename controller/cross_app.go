package main

import (
	"context"
	"net/http"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// ListCrossAppReleases is GET /cluster/cross-app-releases (cluster key or a
// cluster admin; authz routes /cluster/* to the cluster check). It lists
// formations with running processes and apps.release_id values whose release
// was minted for another app. Controllers before CORE-18 allowed a tenant to
// make them; current controllers refuse new ones, so this is the audit for
// rows left behind (CORE-52). flynn-host cross-app:list prints it.
func (c *controllerAPI) ListCrossAppReleases(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	rows, err := c.formationRepo.ListCrossApp()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if rows == nil {
		rows = []*ct.CrossAppRelease{}
	}
	httphelper.JSON(w, http.StatusOK, rows)
}

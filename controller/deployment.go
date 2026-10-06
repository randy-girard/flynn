package main

import (
	"net/http"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetDeployment(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	deployment, err := c.deploymentRepo.Get(params.ByName("deployment_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, deployment)
}

func (c *controllerAPI) CreateDeployment(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var rid releaseID
	if err := httphelper.DecodeJSON(req, &rid); err != nil {
		respondWithError(w, err)
		return
	}
	app := c.getApp(ctx)
	if err := c.rejectIfSuspended(app.OwnerAccount); err != nil {
		respondWithError(w, err)
		return
	}
	appID := app.ID

	d, err := c.deploymentRepo.Add(appID, rid.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, d)
}

func (c *controllerAPI) ListDeployments(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	page, err := parseListPage(req)
	if err != nil {
		respondWithError(w, err)
		return
	}
	var list []*ct.Deployment
	if page.paged() {
		list, err = c.deploymentRepo.ListCount(app.ID, page.Before, page.BeforeID, page.Count)
	} else {
		list, err = c.deploymentRepo.List(app.ID)
	}
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, list)
}

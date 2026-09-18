package main

import (
	"fmt"
	"net/http"

	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/schema"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/postgres"
	"golang.org/x/net/context"
)

type releaseID struct {
	ID string `json:"id"`
}

func (c *controllerAPI) CreateRelease(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var release ct.Release
	if err := httphelper.DecodeJSON(req, &release); err != nil {
		respondWithError(w, err)
		return
	}

	tok := authz.TokenFromContext(ctx)
	var app *ct.App
	if release.AppID != "" {
		data, err := c.appRepo.Get(release.AppID)
		if err != nil {
			respondWithError(w, err)
			return
		}
		app = data.(*ct.App)
		if !authz.SystemAppAllowed(tok, app.System()) {
			httphelper.Forbidden(w, "system apps require the cluster controller credential")
			return
		}
		if !authz.HTTPAllowed(tok, http.MethodPost, "/apps/"+app.ID+"/releases") &&
			!authz.HTTPAllowed(tok, http.MethodPost, "/apps/"+app.Name+"/releases") {
			httphelper.Forbidden(w, "this credential is not allowed to create a release for this app")
			return
		}
		if !authz.CanManageInternalProcessLimits(tok, app.ID) && !authz.CanManageInternalProcessLimits(tok, app.Name) {
			prev, err := c.appRepo.GetRelease(app.ID)
			if err != nil {
				prev = nil
			}
			release.Processes = preserveInternalProcessTypes(release.Processes, prev)
		}
	}

	if err := schema.Validate(&release); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.releaseRepo.Add(&release); err != nil {
		respondWithError(w, err)
		return
	}
	if hideInternalLimits(ctx, app) {
		redacted := redactRelease(&release)
		httphelper.JSON(w, 200, redacted)
		return
	}
	httphelper.JSON(w, 200, &release)
}

func (c *controllerAPI) GetAppReleases(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	list, err := c.releaseRepo.AppList(app.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if hideInternalLimits(ctx, app) {
		list = redactReleases(list)
	}
	httphelper.JSON(w, 200, list)
}

func (c *controllerAPI) SetAppRelease(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var rid releaseID
	if err := httphelper.DecodeJSON(req, &rid); err != nil {
		respondWithError(w, err)
		return
	}

	rel, err := c.releaseRepo.Get(rid.ID)
	if err != nil {
		if err == ErrNotFound {
			err = ct.ValidationError{
				Message: fmt.Sprintf("could not find release with ID %s", rid.ID),
			}
		}
		respondWithError(w, err)
		return
	}
	release := rel.(*ct.Release)

	if err := schema.Validate(release); err != nil {
		respondWithError(w, err)
		return
	}

	app := c.getApp(ctx)
	c.appRepo.SetRelease(app, release.ID)
	if hideInternalLimits(ctx, app) {
		release = redactRelease(release)
	}
	httphelper.JSON(w, 200, release)
}

func (c *controllerAPI) GetAppRelease(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	release, err := c.appRepo.GetRelease(app.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if hideInternalLimits(ctx, app) {
		release = redactRelease(release)
	}
	httphelper.JSON(w, 200, release)
}

func (c *controllerAPI) DeleteRelease(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	release, err := c.getRelease(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.releaseRepo.Delete(app, release); err != nil {
		if postgres.IsPostgresCode(err, postgres.CheckViolation) {
			err = ct.ValidationError{
				Message: "cannot delete current app release",
			}
		}
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

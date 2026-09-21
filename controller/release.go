package main

import (
	"fmt"
	"net/http"

	"github.com/randy-girard/flynn/controller/authorizer"
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
	release := &ct.Release{}
	if err := httphelper.DecodeJSON(req, release); err != nil {
		respondWithError(w, err)
		return
	}

	tok := authz.TokenFromContext(ctx)
	if release.AppID == "" {
		if tok == nil || !tok.HasClusterAdmin() {
			httphelper.Forbidden(w, "app_id is required to create a release")
			return
		}
	}

	var app *ct.App
	if release.AppID != "" {
		data, err := c.appRepo.Get(release.AppID)
		if err != nil {
			respondWithError(w, err)
			return
		}
		app = data.(*ct.App)
		if !mayCreateReleaseForApp(tok, app) {
			if !authz.SystemAppAllowed(tok, app.System()) {
				httphelper.Forbidden(w, "system apps require the cluster controller credential")
				return
			}
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
		sanitizeReleaseProcesses(app, release)
	}

	if err := schema.Validate(release); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.releaseRepo.Add(release); err != nil {
		respondWithError(w, err)
		return
	}
	if hideInternalLimits(ctx, app) {
		httphelper.JSON(w, 200, redactRelease(release))
		return
	}
	httphelper.JSON(w, 200, release)
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
	if !releaseBelongsToApp(release, app) {
		httphelper.Forbidden(w, "release does not belong to this app")
		return
	}
	if stripped := sanitizeReleaseProcesses(app, release); stripped {
		if err := c.releaseRepo.UpdateProcesses(release.ID, release.Processes); err != nil {
			respondWithError(w, err)
			return
		}
	}
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

// mayCreateReleaseForApp reports whether tok may mint a release for app.
// Cluster admins may; app-scoped tokens need env:write on this app. Scale-only
// is not enough. System and platform apps still require the cluster credential.
func mayCreateReleaseForApp(tok *authorizer.Token, app *ct.App) bool {
	if app == nil {
		return false
	}
	if !authz.SystemAppAllowed(tok, app.System()) {
		return false
	}
	if tok != nil && tok.HasClusterAdmin() {
		return true
	}
	if authz.IsPlatformAppName(app.Name) {
		return false
	}
	return authz.CanCreateReleaseForApp(tok, app.ID) || authz.CanCreateReleaseForApp(tok, app.Name)
}

// releaseBelongsToApp is true when the release is unscoped (legacy / admin
// created without app_id) or was minted for this app.
func releaseBelongsToApp(release *ct.Release, app *ct.App) bool {
	if release == nil || app == nil {
		return false
	}
	return release.AppID == "" || release.AppID == app.ID
}

// sanitizeReleaseProcesses strips host_network and other privileged process
// fields on non-system apps. System apps are left unchanged. Returns whether
// privileged fields were present so callers persist before attach.
func sanitizeReleaseProcesses(app *ct.App, release *ct.Release) bool {
	if app == nil || release == nil || app.System() {
		return false
	}
	needPersist := processTypesPrivileged(release.Processes)
	release.Processes = stripPrivilegedProcessTypes(release.Processes)
	return needPersist
}

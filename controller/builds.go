package main

import (
	"net/http"
	"time"

	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/buildadmit"
	"github.com/randy-girard/flynn/controller/buildtoken"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func clusterKeyOnly(ctx context.Context) bool {
	tok := authz.TokenFromContext(ctx)
	return tok != nil && tok.ClusterKey
}

func (c *controllerAPI) CreateBuild(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !clusterKeyOnly(ctx) {
		httphelper.Forbidden(w, "build creation requires the cluster controller key")
		return
	}
	var body ct.BuildRequest
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if c.tokenSigner == nil {
		respondWithError(w, ct.ValidationError{Field: "token", Message: "ACCESS_TOKEN_SIGNING_KEY is required to mint a scoped build token"})
		return
	}
	app := c.getApp(ctx)
	release, err := c.releaseRepo.Get(body.ReleaseID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	rel := release.(*ct.Release)
	if rel.AppID != "" && rel.AppID != app.ID {
		respondWithError(w, ct.ValidationError{Field: "release", Message: "does not belong to this app"})
		return
	}
	settings, err := c.runtimeProfileRepo.Settings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	clusterLimit, accountLimit := settings.BuildLimits()
	job, err := c.jobRepo.CreateBuild(app, &body, buildadmit.Limits{Cluster: clusterLimit, Account: accountLimit})
	if err != nil {
		if capErr, ok := err.(buildadmit.CapacityError); ok {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.RatelimitedErrorCode,
				Message: capErr.Error(),
				Retry:   false,
			})
			return
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, job)
}

func (c *controllerAPI) GetBuildSpec(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !clusterKeyOnly(ctx) {
		httphelper.Forbidden(w, "build specs require the cluster controller key")
		return
	}
	app := c.getApp(ctx)
	params, _ := ctxhelper.ParamsFromContext(ctx)
	job, err := c.jobRepo.GetInApp(app.ID, params.ByName("jobs_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	if !ct.IsBuilderProcessType(job.Type) {
		respondWithError(w, ct.ErrNotFound)
		return
	}
	spec, err := c.jobRepo.GetBuildSpec(job.UUID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	spec.JobID = job.UUID
	httphelper.JSON(w, http.StatusOK, spec)
}

func (c *controllerAPI) MintBuildToken(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !clusterKeyOnly(ctx) {
		httphelper.Forbidden(w, "build tokens require the cluster controller key")
		return
	}
	if c.tokenSigner == nil {
		respondWithError(w, ct.ValidationError{Field: "token", Message: "ACCESS_TOKEN_SIGNING_KEY is not configured"})
		return
	}
	app := c.getApp(ctx)
	params, _ := ctxhelper.ParamsFromContext(ctx)
	job, err := c.jobRepo.GetInApp(app.ID, params.ByName("jobs_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	if !ct.IsBuilderProcessType(job.Type) || (job.State != ct.JobStatePending && job.State != ct.JobStateStarting) {
		respondWithError(w, ct.ValidationError{Field: "job", Message: "is not a compile that is about to start"})
		return
	}
	spec, err := c.jobRepo.GetBuildSpec(job.UUID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	ttl := time.Duration(spec.TimeoutSeconds) * time.Second
	token, err := buildtoken.Mint(c.tokenSigner, app, ttl)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, map[string]string{"token": token})
}

func (c *controllerAPI) ListPendingBuilds(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !clusterKeyOnly(ctx) {
		httphelper.Forbidden(w, "pending builds require the cluster controller key")
		return
	}
	// Pending, blocked, starting, and up. The scheduler uses this to admit
	// without exceeding the cap after a restart.
	jobs, err := c.jobRepo.ListPendingBuilds()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if jobs == nil {
		jobs = []*ct.Job{}
	}
	httphelper.JSON(w, http.StatusOK, jobs)
}

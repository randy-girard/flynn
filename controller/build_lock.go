package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// AcquireBuildLock takes or renews the app's build lease (EDGE-09). A live
// lease held by someone else is 409 with that lease in the error detail, so
// the refused push can say what it is waiting on.
func (c *controllerAPI) AcquireBuildLock(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.BuildLock
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	body.Holder = strings.TrimSpace(body.Holder)
	if body.Holder == "" {
		respondWithError(w, ct.ValidationError{Field: "holder", Message: "must not be empty"})
		return
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	if body.TTLSeconds <= 0 || ttl > ct.MaxBuildLockTTL {
		respondWithError(w, ct.ValidationError{Field: "ttl_seconds", Message: "must be between 1 and " + ct.MaxBuildLockTTL.String()})
		return
	}
	app := c.getApp(ctx)
	lock, held, err := c.buildLockRepo.Acquire(app.ID, body.Holder, body.Description, ttl)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if held != nil {
		detail, _ := json.Marshal(held)
		httphelper.Error(w, httphelper.JSONError{
			Code:    httphelper.ConflictErrorCode,
			Message: (&ct.BuildLockHeldError{Lock: held}).Error(),
			Detail:  detail,
		})
		return
	}
	httphelper.JSON(w, 200, lock)
}

// ReleaseBuildLock drops the app's build lease if the named holder has it.
// Releasing a lease that is gone or held by someone else is not an error.
func (c *controllerAPI) ReleaseBuildLock(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	if err := c.buildLockRepo.Release(c.getApp(ctx).ID, params.ByName("holder")); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

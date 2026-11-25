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
	ttl, err := validateBuildLock(&body)
	if err != nil {
		respondWithError(w, err)
		return
	}
	lock, held, err := c.buildLockRepo.Acquire(c.getApp(ctx).ID, body.Holder, body.Description, ttl)
	respondWithBuildLock(w, lock, held, err)
}

// validateBuildLock checks a lease request: a holder and a bounded TTL.
func validateBuildLock(body *ct.BuildLock) (time.Duration, error) {
	body.Holder = strings.TrimSpace(body.Holder)
	if body.Holder == "" {
		return 0, ct.ValidationError{Field: "holder", Message: "must not be empty"}
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	if body.TTLSeconds <= 0 || ttl > ct.MaxBuildLockTTL {
		return 0, ct.ValidationError{Field: "ttl_seconds", Message: "must be between 1 and " + ct.MaxBuildLockTTL.String()}
	}
	return ttl, nil
}

// respondWithBuildLock answers an acquire: the lease, or 409 with the other
// holder's lease in the error detail.
func respondWithBuildLock(w http.ResponseWriter, lock, held *ct.BuildLock, err error) {
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

// AcquireKeyedBuildLock is AcquireBuildLock for a lease that is not one
// app's (EDGE-40), named by a namespaced key such as "pipeline:shop".
func (c *controllerAPI) AcquireKeyedBuildLock(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	key := params.ByName("key")
	if !ct.BuildLockKeyPattern.MatchString(key) {
		respondWithError(w, ct.ValidationError{Field: "key", Message: "must be <namespace>:<id>, for example pipeline:shop"})
		return
	}
	var body ct.BuildLock
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	ttl, err := validateBuildLock(&body)
	if err != nil {
		respondWithError(w, err)
		return
	}
	lock, held, err := c.buildLockRepo.AcquireKey(key, body.Holder, body.Description, ttl)
	respondWithBuildLock(w, lock, held, err)
}

// ReleaseKeyedBuildLock drops a keyed lease if the named holder has it.
func (c *controllerAPI) ReleaseKeyedBuildLock(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	key := params.ByName("key")
	if !ct.BuildLockKeyPattern.MatchString(key) {
		respondWithError(w, ct.ValidationError{Field: "key", Message: "must be <namespace>:<id>, for example pipeline:shop"})
		return
	}
	if err := c.buildLockRepo.ReleaseKey(key, params.ByName("holder")); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

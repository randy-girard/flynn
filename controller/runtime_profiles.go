package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func (c *controllerAPI) ListRuntimeProfiles(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	list, err := c.runtimeProfileRepo.List()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if list == nil {
		list = []*ct.RuntimeProfile{}
	}
	httphelper.JSON(w, 200, list)
}

func (c *controllerAPI) GetRuntimeProfile(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	p, err := c.runtimeProfileRepo.Get(params.ByName("runtimes_id"))
	if err != nil {
		if err == data.ErrNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, p)
}

func (c *controllerAPI) CreateRuntimeProfile(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var p ct.RuntimeProfile
	if err := httphelper.DecodeJSON(req, &p); err != nil {
		respondWithError(w, err)
		return
	}
	if err := validateRuntimeProfile(&p, false); err != nil {
		respondWithError(w, err)
		return
	}
	p.Builtin = false
	if err := c.runtimeProfileRepo.Add(&p); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &p)
}

func (c *controllerAPI) UpdateRuntimeProfile(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	var p ct.RuntimeProfile
	if err := httphelper.DecodeJSON(req, &p); err != nil {
		respondWithError(w, err)
		return
	}
	p.ID = params.ByName("runtimes_id")
	if err := validateRuntimeProfile(&p, true); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.runtimeProfileRepo.Update(&p); err != nil {
		if err == data.ErrNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &p)
}

func (c *controllerAPI) DeleteRuntimeProfile(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	if err := c.runtimeProfileRepo.Delete(params.ByName("runtimes_id")); err != nil {
		if err == data.ErrNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	w.WriteHeader(200)
}

func (c *controllerAPI) GetRuntimeSettings(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	s, err := c.runtimeProfileRepo.Settings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, s)
}

func (c *controllerAPI) UpdateRuntimeSettings(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		AllowCustomLimits bool    `json:"allow_custom_limits"`
		MaxProcesses      int     `json:"max_processes"`
		ReserveResources  bool    `json:"reserve_resources"`
		BlobGCKeep        *int    `json:"blob_gc_keep"`
		BlobGCMaxAge      *string `json:"blob_gc_max_age"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	cur, err := c.runtimeProfileRepo.Settings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	s := ct.RuntimeSettings{
		AllowCustomLimits: body.AllowCustomLimits,
		MaxProcesses:      body.MaxProcesses,
		ReserveResources:  body.ReserveResources,
		BlobGCKeep:        cur.BlobGCKeepOrDefault(),
		BlobGCMaxAge:      cur.BlobGCMaxAge,
	}
	if body.BlobGCKeep != nil {
		if *body.BlobGCKeep < 0 {
			respondWithError(w, ct.ValidationError{Field: "blob_gc_keep", Message: "must be 0 or more"})
			return
		}
		s.BlobGCKeep = *body.BlobGCKeep
	}
	if body.BlobGCMaxAge != nil {
		age := strings.TrimSpace(*body.BlobGCMaxAge)
		if age != "" && age != "0" {
			if _, err := time.ParseDuration(age); err != nil {
				respondWithError(w, ct.ValidationError{Field: "blob_gc_max_age", Message: "must be a Go duration such as 720h"})
				return
			}
		}
		s.BlobGCMaxAge = age
	}
	if err := c.runtimeProfileRepo.UpdateSettings(&s); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &s)
}

func validateRuntimeProfile(p *ct.RuntimeProfile, allowEmptyName bool) error {
	if err := resource.ValidateProfileName(p.Name); err != nil && !(allowEmptyName && strings.TrimSpace(p.Name) == "") {
		return ct.ValidationError{Field: "name", Message: err.Error()}
	}
	if p.Memory <= 0 {
		return ct.ValidationError{Field: "memory", Message: "must be greater than zero"}
	}
	if p.CPU <= 0 {
		return ct.ValidationError{Field: "cpu", Message: "must be greater than zero"}
	}
	return nil
}

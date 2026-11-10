package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/buildadmit"
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

// maxProcessesField tells an omitted max_processes from JSON null and from a number.
// Omitted keeps the stored value. Null is unlimited. 0 is rejected.
type maxProcessesField struct {
	set       bool
	unlimited bool
	n         int
}

func (m *maxProcessesField) UnmarshalJSON(b []byte) error {
	m.set = true
	if string(b) == "null" {
		m.unlimited = true
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	if n < 1 {
		return fmt.Errorf("max_processes must be a positive integer or null")
	}
	m.n = n
	return nil
}

func (c *controllerAPI) UpdateRuntimeSettings(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		AllowCustomLimits bool              `json:"allow_custom_limits"`
		MaxProcesses      maxProcessesField `json:"max_processes"`
		ReserveResources  bool              `json:"reserve_resources"`
		BlobGCKeep        *int              `json:"blob_gc_keep"`
		BlobGCMaxAge      *string           `json:"blob_gc_max_age"`
		MaxRestarts       *int              `json:"max_restarts"`

		JobRestartInterval    *string `json:"job_restart_interval"`
		SingleInstanceRestart *bool   `json:"single_instance_restart"`
		AllowUserScale        *bool   `json:"allow_user_scale"`
		AllowUserScaleTags    *bool   `json:"allow_user_scale_tags"`
		BuildClusterLimit     *int    `json:"build_cluster_limit"`
		BuildAccountLimit     *int    `json:"build_account_limit"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		if strings.Contains(err.Error(), "max_processes") {
			respondWithError(w, ct.ValidationError{Field: "max_processes", Message: "must be a positive integer or null"})
			return
		}
		respondWithError(w, err)
		return
	}
	cur, err := c.runtimeProfileRepo.Settings()
	if err != nil {
		respondWithError(w, err)
		return
	}
	var maxProcesses *int
	if body.MaxProcesses.set {
		if !body.MaxProcesses.unlimited {
			n := body.MaxProcesses.n
			maxProcesses = &n
		}
	} else if cur.MaxProcesses != nil {
		n := *cur.MaxProcesses
		maxProcesses = &n
	}
	s := ct.RuntimeSettings{
		AllowCustomLimits: body.AllowCustomLimits,
		MaxProcesses:      maxProcesses,
		ReserveResources:  body.ReserveResources,
		BlobGCKeep:        cur.BlobGCKeepOrDefault(),
		BlobGCMaxAge:      cur.BlobGCMaxAge,
		MaxRestarts:       cur.MaxRestarts,

		JobRestartInterval:    cur.JobRestartInterval,
		SingleInstanceRestart: cur.SingleInstanceRestart,
		AllowUserScale:        cur.AllowUserScale,
		AllowUserScaleTags:    cur.AllowUserScaleTags,
	}
	clusterLimit, accountLimit := cur.BuildLimits()
	if body.BuildClusterLimit != nil {
		clusterLimit = *body.BuildClusterLimit
	}
	if body.BuildAccountLimit != nil {
		accountLimit = *body.BuildAccountLimit
	}
	if err := buildadmit.ValidLimits(buildadmit.Limits{Cluster: clusterLimit, Account: accountLimit}); err != nil {
		respondWithError(w, ct.ValidationError{Field: "build_limits", Message: err.Error()})
		return
	}
	s.BuildClusterLimit = clusterLimit
	s.BuildAccountLimit = accountLimit
	if body.AllowUserScale != nil {
		s.AllowUserScale = *body.AllowUserScale
	}
	if body.AllowUserScaleTags != nil {
		s.AllowUserScaleTags = *body.AllowUserScaleTags
	}
	if body.SingleInstanceRestart != nil {
		s.SingleInstanceRestart = *body.SingleInstanceRestart
	}
	if body.JobRestartInterval != nil && strings.TrimSpace(*body.JobRestartInterval) != "" {
		interval, _, err := ct.ParseJobRestartInterval(*body.JobRestartInterval)
		if err != nil {
			respondWithError(w, ct.ValidationError{Field: "job_restart_interval", Message: err.Error()})
			return
		}
		s.JobRestartInterval = interval
	}
	if body.MaxRestarts != nil {
		if *body.MaxRestarts < 0 {
			respondWithError(w, ct.ValidationError{Field: "max_restarts", Message: "must be 0 (unlimited) or more"})
			return
		}
		s.MaxRestarts = *body.MaxRestarts
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
	if body.BlobGCKeep != nil || body.BlobGCMaxAge != nil {
		c.scheduleAllAppGarbageCollection()
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

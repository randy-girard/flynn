package main

import (
	"net/http"
	"strings"
	"sync"

	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// dbRuntimeCatalog is the cluster copy of database runtimes. flynn-host
// publishes its file here. Plugin jobs use the cluster controller key.
// The web UI writes here only for a cluster admin.
var (
	dbRuntimeMu  sync.Mutex
	dbRuntimeCat = dbruntime.BuiltinCatalog()
)

func resetDBRuntimes() {
	dbRuntimeMu.Lock()
	dbRuntimeCat = dbruntime.BuiltinCatalog()
	dbRuntimeMu.Unlock()
}

func canManageDBRuntimes(ctx context.Context) bool {
	tok := authz.TokenFromContext(ctx)
	if tok == nil {
		return false
	}
	// ClusterKey is flynn-host and plugin jobs it starts. HasClusterAdmin
	// also covers an explicit cluster:admin scope from the dashboard.
	return dbruntime.CanManage(tok.ClusterKey, tok.HasClusterAdmin() && !tok.ClusterKey) || tok.ClusterKey
}

func (c *controllerAPI) ListDBRuntimes(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	dbRuntimeMu.Lock()
	cat := dbRuntimeCat
	dbRuntimeMu.Unlock()
	httphelper.JSON(w, 200, cat)
}

func (c *controllerAPI) CreateDBRuntime(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !canManageDBRuntimes(ctx) {
		httphelper.Forbidden(w, "creating a database runtime requires flynn-host or a cluster admin")
		return
	}
	var r dbruntime.Runtime
	if err := httphelper.DecodeJSON(req, &r); err != nil {
		respondWithError(w, err)
		return
	}
	dbRuntimeMu.Lock()
	defer dbRuntimeMu.Unlock()
	if err := dbRuntimeCat.Create(r); err != nil {
		respondWithError(w, ct.ValidationError{Field: "runtime", Message: err.Error()})
		return
	}
	httphelper.JSON(w, 201, r)
}

func (c *controllerAPI) ReplaceDBRuntimes(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !canManageDBRuntimes(ctx) {
		httphelper.Forbidden(w, "replacing database runtimes requires flynn-host or a cluster admin")
		return
	}
	var cat dbruntime.Catalog
	if err := httphelper.DecodeJSON(req, &cat); err != nil {
		respondWithError(w, err)
		return
	}
	if len(cat.Runtimes) == 0 {
		respondWithError(w, ct.ValidationError{Field: "runtimes", Message: "catalog is empty"})
		return
	}
	dbRuntimeMu.Lock()
	dbRuntimeCat = cat
	dbRuntimeMu.Unlock()
	httphelper.JSON(w, 200, cat)
}

func (c *controllerAPI) UpdateDBRuntime(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !canManageDBRuntimes(ctx) {
		httphelper.Forbidden(w, "updating a database runtime requires flynn-host or a cluster admin")
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	var body dbruntime.Runtime
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	fields := dbruntime.UpdateFields{CPU: &body.CPU, Memory: &body.Memory, Disk: &body.Disk}
	if n := strings.TrimSpace(body.Name); n != "" {
		fields.Name = &n
	}
	dbRuntimeMu.Lock()
	defer dbRuntimeMu.Unlock()
	updated, err := dbRuntimeCat.Update(params.ByName("engine"), params.ByName("name"), fields)
	if err != nil {
		respondWithError(w, ct.ValidationError{Field: "runtime", Message: err.Error()})
		return
	}
	httphelper.JSON(w, 200, updated)
}

func (c *controllerAPI) DeleteDBRuntime(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !canManageDBRuntimes(ctx) {
		httphelper.Forbidden(w, "removing a database runtime requires flynn-host or a cluster admin")
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	dbRuntimeMu.Lock()
	defer dbRuntimeMu.Unlock()
	if err := dbRuntimeCat.Remove(params.ByName("engine"), params.ByName("name")); err != nil {
		respondWithError(w, ct.ValidationError{Field: "runtime", Message: err.Error()})
		return
	}
	w.WriteHeader(200)
}

func (c *controllerAPI) UpdateDBRuntimeSettings(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if !canManageDBRuntimes(ctx) {
		httphelper.Forbidden(w, "updating database runtime settings requires flynn-host or a cluster admin")
		return
	}
	var body struct {
		AllowCustomSizes bool `json:"allow_custom_sizes"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	dbRuntimeMu.Lock()
	dbRuntimeCat.AllowCustomSizes = body.AllowCustomSizes
	cat := dbRuntimeCat
	dbRuntimeMu.Unlock()
	httphelper.JSON(w, 200, cat)
}

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

// memDBRuntimes is the in-process catalog used when the controller is
// constructed without postgres (unit tests). The live API uses dbRuntimeRepo.
var (
	memDBRuntimeMu sync.Mutex
	memDBRuntimes  = dbruntime.EmptyCatalog()
)

func resetDBRuntimes() {
	memDBRuntimeMu.Lock()
	memDBRuntimes = dbruntime.EmptyCatalog()
	memDBRuntimeMu.Unlock()
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

func (c *controllerAPI) loadDBRuntimes() (dbruntime.Catalog, error) {
	if c != nil && c.dbRuntimeRepo != nil {
		return c.dbRuntimeRepo.Load()
	}
	memDBRuntimeMu.Lock()
	defer memDBRuntimeMu.Unlock()
	return memDBRuntimes, nil
}

func (c *controllerAPI) replaceDBRuntimes(cat dbruntime.Catalog) error {
	if c != nil && c.dbRuntimeRepo != nil {
		return c.dbRuntimeRepo.Replace(cat)
	}
	memDBRuntimeMu.Lock()
	memDBRuntimes = cat
	memDBRuntimeMu.Unlock()
	return nil
}

func (c *controllerAPI) mutateDBRuntimes(fn func(*dbruntime.Catalog) error) (dbruntime.Catalog, error) {
	if c != nil && c.dbRuntimeRepo != nil {
		return c.dbRuntimeRepo.Mutate(fn)
	}
	memDBRuntimeMu.Lock()
	defer memDBRuntimeMu.Unlock()
	if err := fn(&memDBRuntimes); err != nil {
		return dbruntime.Catalog{}, err
	}
	return memDBRuntimes, nil
}

func (c *controllerAPI) ListDBRuntimes(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	cat, err := c.loadDBRuntimes()
	if err != nil {
		respondWithError(w, err)
		return
	}
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
	cat, err := c.mutateDBRuntimes(func(cat *dbruntime.Catalog) error {
		return cat.Create(r)
	})
	if err != nil {
		respondWithError(w, ct.ValidationError{Field: "runtime", Message: err.Error()})
		return
	}
	created, _ := cat.Find(r.Engine, r.Name)
	httphelper.JSON(w, 201, created)
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
	if err := c.replaceDBRuntimes(cat); err != nil {
		respondWithError(w, err)
		return
	}
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
	var updated dbruntime.Runtime
	_, err := c.mutateDBRuntimes(func(cat *dbruntime.Catalog) error {
		got, err := cat.Update(params.ByName("engine"), params.ByName("name"), fields)
		if err != nil {
			return err
		}
		updated = got
		return nil
	})
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
	_, err := c.mutateDBRuntimes(func(cat *dbruntime.Catalog) error {
		return cat.Remove(params.ByName("engine"), params.ByName("name"))
	})
	if err != nil {
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
	cat, err := c.mutateDBRuntimes(func(cat *dbruntime.Catalog) error {
		cat.AllowCustomSizes = body.AllowCustomSizes
		return nil
	})
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, cat)
}

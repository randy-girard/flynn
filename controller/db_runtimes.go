package main

import (
	"net/http"
	"sync"

	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
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

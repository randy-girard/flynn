package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// auditHTTPAction names a mutating request for core_audit. ok is false for
// reads, internal traffic, and routes whose handlers audit themselves.
// appRef is the app id or name from the path, if any.
func auditHTTPAction(method, path string) (action, appRef string, ok bool) {
	p := strings.Split(strings.Trim(path, "/"), "/")
	n := len(p)
	verb := map[string]string{http.MethodPost: "create", http.MethodPut: "update", http.MethodDelete: "delete"}[method]
	if verb == "" {
		return "", "", false
	}
	switch p[0] {
	case "apps":
		if n == 1 && method == http.MethodPost {
			return "app.create", "", true
		}
		if n == 2 && method == http.MethodDelete {
			return "app.delete", p[1], true
		}
		if n < 3 {
			return "", "", false
		}
		ref := p[1]
		switch sub := p[2]; {
		case sub == "meta" && method == http.MethodPost:
			action = "app.update"
		case sub == "release" && method == http.MethodPut:
			action = "app.release.set"
		case sub == "deploy" && method == http.MethodPost:
			action = "deployment.create"
		case sub == "formations" && method != http.MethodPost:
			action = "formation." + map[string]string{http.MethodPut: "set", http.MethodDelete: "delete"}[method]
		case sub == "scale" && method == http.MethodPut:
			action = "scale.request"
		case sub == "jobs" && n == 3 && method == http.MethodPost:
			action = "job.run"
		case sub == "jobs" && n == 4 && method == http.MethodDelete:
			action = "job.kill"
		case sub == "routes":
			action = "route." + verb
		case sub == "releases" && method == http.MethodDelete:
			action = "release.delete"
		case sub == "github" && n == 3:
			action = map[string]string{http.MethodPut: "github.connect", http.MethodDelete: "github.disconnect"}[method]
		case sub == "github" && n == 4 && p[3] == "deploy":
			action = "github.deploy"
		case sub == "volumes" && n == 5 && p[4] == "decommission":
			action = "volume.decommission"
		}
		return action, ref, action != ""
	case "releases":
		return "release.create", "", n == 1 && method == http.MethodPost
	case "providers":
		if n == 3 && p[2] == "resources" && method == http.MethodPost {
			return "resource.create", "", true
		}
		if n == 4 && p[2] == "resources" && method == http.MethodDelete {
			return "resource.delete", "", true
		}
		if n == 6 && p[4] == "apps" && method != http.MethodPost {
			return map[string]string{http.MethodPut: "resource.app.add", http.MethodDelete: "resource.app.remove"}[method], p[5], true
		}
	case "tokens", "sinks", "runtimes":
		return strings.TrimSuffix(p[0], "s") + "." + verb, "", method != http.MethodPut || n == 2
	case "db-runtimes":
		return "db_runtime." + verb, "", true
	case "cluster":
		if n == 2 && method == http.MethodPut && p[1] != "policy" {
			return "cluster." + strings.ReplaceAll(p[1], "-", "_") + ".set", "", true
		}
	case "acme", "github":
		if n == 2 && method == http.MethodPut && (p[1] == "config" || p[1] == "app") {
			return p[0] + "." + p[1] + ".set", "", true
		}
	}
	return "", "", false
}

// auditRequest records a successful mutating request in core_audit.
// Request bodies are not stored: they can hold env values and keys.
func (c *controllerAPI) auditRequest(tok *authorizer.Token, req *http.Request, status int) {
	if status < 200 || status > 299 {
		return
	}
	action, ref, ok := auditHTTPAction(req.Method, req.URL.Path)
	if !ok {
		return
	}
	var appID string
	if ref != "" && c.appRepo != nil {
		if a, err := c.appRepo.Get(ref); err == nil {
			appID = a.(*ct.App).ID
		}
	}
	data := map[string]interface{}{"method": req.Method, "path": req.URL.Path, "status": status}
	if err := c.tenancy.Audit(actorFromToken(tok), action, "", appID, data); err != nil {
		logger.Error("error recording audit entry", "action", action, "err", err)
	}
}

var auditGRPCActions = map[string]string{
	api.Controller_UpdateApp_FullMethodName:        "app.update",
	api.Controller_CreateScale_FullMethodName:      "scale.request",
	api.Controller_CreateRelease_FullMethodName:    "release.create",
	api.Controller_CreateDeployment_FullMethodName: "deployment.create",
}

// auditGRPC records a successful mutating gRPC call in core_audit.
func (c *controllerAPI) auditGRPC(tok *authorizer.Token, method string, req interface{}) {
	action, ok := auditGRPCActions[method]
	if !ok || c.tenancy == nil {
		return
	}
	var name string
	switch r := req.(type) {
	case interface{ GetParent() string }:
		name = r.GetParent()
	case *api.UpdateAppRequest:
		name = r.GetApp().GetName()
	}
	appID := api.ParseIDFromName(name, "apps")
	data := map[string]interface{}{"method": method}
	if err := c.tenancy.Audit(actorFromToken(tok), action, "", appID, data); err != nil {
		logger.Error("error recording audit entry", "action", action, "err", err)
	}
}

// ListClusterAudit is GET /cluster/audit?after=<id>&limit=<n>, oldest first.
func (c *controllerAPI) ListClusterAudit(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	after, _ := strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	list, err := c.tenancy.ListAudit(after, limit)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, list)
}

// PruneClusterAudit is DELETE /cluster/audit?before=<RFC3339>&max_id=<id>.
// max_id lets a consumer delete only rows it has already copied.
func (c *controllerAPI) PruneClusterAudit(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	before, err := time.Parse(time.RFC3339, req.URL.Query().Get("before"))
	if err != nil {
		httphelper.ValidationError(w, "before", "must be an RFC3339 time")
		return
	}
	maxID, err := strconv.ParseInt(req.URL.Query().Get("max_id"), 10, 64)
	if err != nil || maxID < 0 {
		httphelper.ValidationError(w, "max_id", "must be a non-negative integer")
		return
	}
	n, err := c.tenancy.PruneAudit(before, maxID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "audit.prune", "", "", map[string]interface{}{
		"before": before, "max_id": maxID, "deleted": n,
	})
	httphelper.JSON(w, 200, map[string]int64{"deleted": n})
}

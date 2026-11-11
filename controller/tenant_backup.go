package main

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

var tenantBackupResourceID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (c *controllerAPI) tenantBackupID(ctx context.Context) (string, error) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	id := strings.TrimSpace(params.ByName("resource_id"))
	if !tenantBackupResourceID.MatchString(id) {
		return "", ct.ValidationError{Field: "resource_id", Message: "resource id must be a uuid"}
	}
	return id, nil
}

func validBackupTimestamp(s string) error {
	s = strings.TrimSpace(s)
	t, err := time.Parse("20060102T150405Z", s)
	if err != nil || t.UTC().Format("20060102T150405Z") != s {
		return ct.ValidationError{Field: "timestamp", Message: "timestamp must be like 20060102T150405Z"}
	}
	return nil
}

func validBackupDatabase(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return ct.ValidationError{Field: "database", Message: "database name is required"}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' {
			continue
		}
		return ct.ValidationError{Field: "database", Message: "database name must be letters, digits, and underscores"}
	}
	if name[0] >= '0' && name[0] <= '9' {
		return ct.ValidationError{Field: "database", Message: "database name cannot start with a digit"}
	}
	switch strings.ToLower(name) {
	case "postgres", "template0", "template1":
		return ct.ValidationError{Field: "database", Message: name + " is a reserved database name"}
	}
	return nil
}

func (c *controllerAPI) ListTenantBackups(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	list, err := c.tenantBackupRepo.List()
	if err != nil {
		respondWithError(w, err)
		return
	}
	if list == nil {
		list = []*ct.TenantBackup{}
	}
	httphelper.JSON(w, 200, list)
}

func (c *controllerAPI) GetTenantBackup(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	st, err := c.tenantBackupRepo.Get(id)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, st)
}

func (c *controllerAPI) BeginTenantBackup(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	var body struct {
		Timestamp string `json:"timestamp"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := validBackupTimestamp(body.Timestamp); err != nil {
		respondWithError(w, err)
		return
	}
	st, started, err := c.tenantBackupRepo.Begin(id, body.Timestamp)
	if err != nil {
		respondWithError(w, err)
		return
	}
	code := 200
	if !started {
		code = http.StatusConflict
	}
	httphelper.JSON(w, code, ct.TenantBackupBegin{Started: started, Status: *st})
}

func (c *controllerAPI) HeartbeatTenantBackup(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	var body struct {
		Timestamp string `json:"timestamp"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := validBackupTimestamp(body.Timestamp); err != nil {
		respondWithError(w, err)
		return
	}
	st, err := c.tenantBackupRepo.Heartbeat(id, body.Timestamp)
	if err == data.ErrTenantBackupLock {
		httphelper.JSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, st)
}

func (c *controllerAPI) FinishTenantBackup(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	var body struct {
		State     string   `json:"state"`
		Error     string   `json:"error"`
		Timestamp string   `json:"timestamp"`
		Databases []string `json:"databases"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := validBackupTimestamp(body.Timestamp); err != nil {
		respondWithError(w, err)
		return
	}
	switch body.State {
	case "succeeded", "failed":
	default:
		respondWithError(w, ct.ValidationError{Field: "state", Message: "state must be succeeded or failed"})
		return
	}
	var databases []string
	if body.State == "succeeded" {
		seen := map[string]bool{}
		for _, name := range body.Databases {
			name = strings.TrimSpace(name)
			if err := validBackupDatabase(name); err != nil {
				respondWithError(w, err)
				return
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			databases = append(databases, name)
		}
		if len(databases) == 0 {
			respondWithError(w, ct.ValidationError{Field: "databases", Message: "a successful backup names at least one database"})
			return
		}
	}
	st, err := c.tenantBackupRepo.Finish(id, body.Timestamp, body.State, body.Error, databases)
	if err == data.ErrTenantBackupLock {
		httphelper.JSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, st)
}

func (c *controllerAPI) FailTenantBackup(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if strings.TrimSpace(body.Error) == "" {
		respondWithError(w, ct.ValidationError{Field: "error", Message: "error is required"})
		return
	}
	if err := c.tenantBackupRepo.Fail(id, body.Error); err != nil {
		respondWithError(w, err)
		return
	}
	st, err := c.tenantBackupRepo.Get(id)
	if err == data.ErrNotFound {
		httphelper.JSON(w, 200, map[string]string{"status": "running"})
		return
	}
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, st)
}

func (c *controllerAPI) ListTenantBackupDumps(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if _, err := c.tenantBackupRepo.Get(id); err != nil {
		respondWithError(w, err)
		return
	}
	list, err := c.tenantBackupRepo.ListDumps(id)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if list == nil {
		list = []ct.TenantBackupDump{}
	}
	httphelper.JSON(w, 200, list)
}

func (c *controllerAPI) DeleteTenantBackupDump(ctx context.Context, w http.ResponseWriter, _ *http.Request) {
	id, err := c.tenantBackupID(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	ts := strings.TrimSpace(params.ByName("timestamp"))
	if err := validBackupTimestamp(ts); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.tenantBackupRepo.DeleteDump(id, ts); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

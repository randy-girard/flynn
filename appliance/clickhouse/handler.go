package clickhouse

import (
	"encoding/json"
	"net/http"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/status"
	"github.com/julienschmidt/httprouter"
	"github.com/inconshreveable/log15"
)

// Handler represents an HTTP handler for the clickhouse-server process.
type Handler struct {
	router *httprouter.Router

	Process     *Process
	Heartbeater discoverd.Heartbeater
	Logger      log15.Logger
}

// NewHandler returns a new instance of Handler.
func NewHandler() *Handler {
	h := &Handler{
		router: httprouter.New(),
		Logger: log15.New(),
	}
	h.router.Handler("GET", status.Path, status.Handler(h.healthStatus))
	h.router.GET("/status", h.handleGetStatus)
	h.router.POST("/stop", h.handlePostStop)

	h.router.GET("/databases", h.handleListDatabases)
	h.router.POST("/databases", h.handleCreateDatabase)
	h.router.GET("/databases/:name", h.handleDescribeDatabase)
	h.router.DELETE("/databases/:name", h.handleDeleteDatabase)
	return h
}

// ServeHTTP serves an HTTP request and returns a response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) { h.router.ServeHTTP(w, req) }

func (h *Handler) healthStatus() status.Status {
	info, err := h.Process.Info()
	if err != nil || !info.Running {
		return status.Unhealthy
	}
	return status.Healthy
}

func (h *Handler) handleGetStatus(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	info, err := h.Process.Info()
	if err != nil {
		h.Logger.Error("error getting clickhouse info", "err", err)
	}
	httphelper.JSON(w, 200, &Status{Process: info})
}

func (h *Handler) handlePostStop(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	if err := h.Heartbeater.Close(); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *Handler) handleListDatabases(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	databases, err := h.Process.DatabaseList()
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, databases)
}

func (h *Handler) handleCreateDatabase(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		httphelper.Error(w, err)
		return
	}
	if err := h.Process.CreateDatabase(body.Name); err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, body)
}

func (h *Handler) handleDescribeDatabase(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	desc, err := h.Process.DescribeDatabase(ps.ByName("name"))
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, map[string]string{"description": desc})
}

func (h *Handler) handleDeleteDatabase(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	if err := h.Process.DeleteDatabase(ps.ByName("name")); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

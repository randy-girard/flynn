package kafka

import (
	"encoding/json"
	"net/http"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/status"
	"github.com/julienschmidt/httprouter"
	"github.com/inconshreveable/log15"
)

// Handler represents an HTTP handler for the kafka broker process. In addition
// to health/lifecycle endpoints it exposes a small managed admin API for
// topics and consumer groups, backed by the local broker.
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

	h.router.GET("/topics", h.handleListTopics)
	h.router.POST("/topics", h.handleCreateTopic)
	h.router.GET("/topics/:name", h.handleDescribeTopic)
	h.router.POST("/topics/:name/config", h.handleConfigureTopic)
	h.router.DELETE("/topics/:name", h.handleDeleteTopic)

	h.router.GET("/consumer-groups", h.handleListGroups)
	h.router.POST("/consumer-groups", h.handleCreateGroup)
	h.router.GET("/consumer-groups/:name", h.handleDescribeGroup)
	h.router.DELETE("/consumer-groups/:name", h.handleDeleteGroup)
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
		h.Logger.Error("error getting kafka info", "err", err)
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

func (h *Handler) handleListTopics(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	topics, err := h.Process.TopicList()
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, topics)
}

func (h *Handler) handleCreateTopic(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var spec TopicSpec
	if err := json.NewDecoder(req.Body).Decode(&spec); err != nil {
		httphelper.Error(w, err)
		return
	}
	if err := h.Process.CreateTopic(spec); err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, spec)
}

func (h *Handler) handleDescribeTopic(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	desc, err := h.Process.DescribeTopic(ps.ByName("name"))
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, map[string]string{"description": desc})
}

func (h *Handler) handleConfigureTopic(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var configs map[string]string
	if err := json.NewDecoder(req.Body).Decode(&configs); err != nil {
		httphelper.Error(w, err)
		return
	}
	if err := h.Process.ConfigureTopic(ps.ByName("name"), configs); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *Handler) handleDeleteTopic(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	if err := h.Process.DeleteTopic(ps.ByName("name")); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *Handler) handleListGroups(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	groups, err := h.Process.GroupList()
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, groups)
}

func (h *Handler) handleCreateGroup(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var body struct {
		Name  string `json:"name"`
		Topic string `json:"topic"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		httphelper.Error(w, err)
		return
	}
	if err := h.Process.CreateGroup(body.Name, body.Topic); err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, body)
}

func (h *Handler) handleDescribeGroup(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	desc, err := h.Process.DescribeGroup(ps.ByName("name"))
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, 200, map[string]string{"description": desc})
}

func (h *Handler) handleDeleteGroup(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	if err := h.Process.DeleteGroup(ps.ByName("name")); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

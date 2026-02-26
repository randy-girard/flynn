package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/downloader"
	"github.com/flynn/flynn/host/logmux"
	host "github.com/flynn/flynn/host/types"
	volumeapi "github.com/flynn/flynn/host/volume/api"
	volumemanager "github.com/flynn/flynn/host/volume/manager"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/keepalive"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/sse"
	"github.com/inconshreveable/log15"
	"github.com/julienschmidt/httprouter"
)

type Host struct {
	state   *State
	backend Backend
	vman    *volumemanager.Manager
	sman    *logmux.SinkManager
	discMan *DiscoverdManager
	volAPI  *volumeapi.HTTPAPI
	id      string
	url     string

	statusMtx sync.RWMutex
	status    *host.HostStatus

	discoverdOnce sync.Once
	networkOnce   sync.Once

	listener net.Listener

	maxJobConcurrency uint64

	authKey string

	log log15.Logger
}

// authMiddleware wraps an http.Handler and requires a valid Auth-Key header
// or Basic auth password matching the host's authKey. If no authKey is
// configured, all requests are allowed (backwards compatibility).
func (h *Host) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.authKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Allow unauthenticated health checks
		if r.URL.Path == "/host/status" && r.Method == "GET" {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("Auth-Key")
		if key == "" {
			// Fall back to Basic auth password
			_, key, _ = r.BasicAuth()
		}

		if key == "" || len(key) != len(h.authKey) ||
			subtle.ConstantTimeCompare([]byte(key), []byte(h.authKey)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="flynn-host"`)
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.UnauthorizedErrorCode,
				Message: "authentication required",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SEC-017: perIPRateLimiter tracks request counts per client IP to prevent
// API abuse and denial-of-service attacks.
type perIPRateLimiter struct {
	mu       sync.Mutex
	requests map[string]int
	limit    int
	window   time.Duration
}

func newPerIPRateLimiter(limit int, window time.Duration) *perIPRateLimiter {
	rl := &perIPRateLimiter{
		requests: make(map[string]int),
		limit:    limit,
		window:   window,
	}
	go func() {
		for range time.Tick(window) {
			rl.mu.Lock()
			rl.requests = make(map[string]int)
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *perIPRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.requests[ip]++
	return rl.requests[ip] <= rl.limit
}

func (h *Host) rateLimitMiddleware(next http.Handler) http.Handler {
	limiter := newPerIPRateLimiter(100, time.Minute) // 100 requests per minute per IP
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exempt health checks from rate limiting
		if r.URL.Path == "/host/status" && r.Method == "GET" {
			next.ServeHTTP(w, r)
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}
		if !limiter.Allow(ip) {
			httphelper.Error(w, httphelper.JSONError{
				Code:    httphelper.RatelimitedErrorCode,
				Message: "too many requests, try again later",
				Retry:   true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

var ErrNotFound = errors.New("host: unknown job")

func (h *Host) StopJob(id string) error {
	log := h.log.New("fn", "StopJob", "job.id", id)

	log.Info("acquiring state database")
	if err := h.state.Acquire(); err != nil {
		log.Error("error acquiring state database", "err", err)
		return err
	}
	defer h.state.Release()

	job := h.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		return ErrNotFound
	}
	switch job.Status {
	case host.StatusStarting:
		log.Info("job status is starting, marking it as stopped")
		h.state.SetForceStop(id)

		// if the job doesn't exist in the backend, mark it as done
		// to avoid it remaining in the starting state indefinitely
		if !h.backend.JobExists(id) {
			h.state.SetStatusDone(id, 0)
		}

		return nil
	case host.StatusRunning:
		log.Info("stopping job")
		return h.backend.Stop(id)
	default:
		log.Warn("job already stopped")
		return errors.New("host: job is already stopped")
	}
}

func (h *Host) SignalJob(id string, sig int) error {
	log := h.log.New("fn", "SignalJob", "job.id", id, "sig", sig)

	job := h.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		return ErrNotFound
	}
	log.Info("signalling job")
	return h.backend.Signal(id, sig)
}

func (h *Host) DiscoverdDeregisterJob(id string) error {
	log := h.log.New("fn", "DiscoverdDeregisterJob", "job.id", id)

	job := h.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		return ErrNotFound
	}
	log.Info("deregistering job")
	return h.backend.DiscoverdDeregister(id)
}

func (h *Host) streamEvents(id string, w http.ResponseWriter) error {
	ch := h.state.AddListener(id)
	defer h.state.RemoveListener(id, ch)
	sse.ServeStream(w, ch, nil)
	return nil
}

func (h *Host) ConfigureNetworking(config *host.NetworkConfig) {
	log := h.log.New("fn", "ConfigureNetworking")

	if config.JobID != "" {
		log.Info("persisting flannel job_id", "job.id", config.JobID)
		if err := h.state.SetPersistentSlot("flannel", config.JobID); err != nil {
			log.Error("error assigning flannel to persistent slot")
		}
	}
	h.networkOnce.Do(func() {
		log.Info("configuring network", "subnet", config.Subnet, "mtu", config.MTU, "resolvers", config.Resolvers)
		if err := h.backend.ConfigureNetworking(config); err != nil {
			log.Error("error configuring network", "err", err)
			shutdown.Fatal(err)
		}

		h.statusMtx.Lock()
		h.status.Network = config
		h.statusMtx.Unlock()
	})
	h.statusMtx.Lock()
	if h.status.Network != nil {
		h.status.Network.JobID = config.JobID
		h.backend.SetNetworkConfig(h.status.Network)
	}
	h.statusMtx.Unlock()
}

func (h *Host) ConfigureDiscoverd(config *host.DiscoverdConfig) {
	log := h.log.New("fn", "ConfigureDiscoverd")

	if config.JobID != "" {
		log.Info("persisting discoverd job_id", "job.id", config.JobID)
		if err := h.state.SetPersistentSlot("discoverd", config.JobID); err != nil {
			log.Error("error assigning discoverd to persistent slot")
		}
	}

	if config.URL != "" && config.DNS != "" {
		go h.discoverdOnce.Do(func() {
			log.Info("connecting to service discovery", "url", config.URL)
			if err := h.discMan.ConnectLocal(config.URL); err != nil {
				log.Error("error connecting to service discovery", "err", err)
				shutdown.Fatal(err)
			}
		})
	}

	h.statusMtx.Lock()
	h.status.Discoverd = config
	h.backend.SetDiscoverdConfig(h.status.Discoverd)
	h.statusMtx.Unlock()

	if config.URL != "" {
		h.volAPI.ConfigureClusterClient(config.URL)
	}
}

type jobAPI struct {
	host                  *Host
	addJobRateLimitBucket *RateLimitBucket
}

func (h *jobAPI) ListJobs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := h.host.streamEvents("all", w); err != nil {
			httphelper.Error(w, err)
		}
		return
	}
	var jobs map[string]*host.ActiveJob
	if r.FormValue("active") == "true" {
		jobs = h.host.state.GetActive()
	} else {
		jobs = h.host.state.Get()
	}
	httphelper.JSON(w, 200, jobs)
}

func (h *jobAPI) GetJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id := ps.ByName("id")
	log := h.host.log.New("fn", "GetJob", "job.id", id)

	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		log.Info("streaming job events")
		if err := h.host.streamEvents(id, w); err != nil {
			log.Error("error streaming job events", "err", err)
			httphelper.Error(w, err)
		}
		return
	}

	job := h.host.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		httphelper.ObjectNotFoundError(w, ErrNotFound.Error())
		return
	}
	httphelper.JSON(w, 200, job)
}

func (h *jobAPI) StopJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id := ps.ByName("id")
	if err := h.host.StopJob(id); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *jobAPI) DiscoverdDeregisterJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id := ps.ByName("id")
	if err := h.host.DiscoverdDeregisterJob(id); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *jobAPI) SignalJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sig := ps.ByName("signal")
	if sig == "" {
		httphelper.ValidationError(w, "sig", "must not be empty")
		return
	}
	sigInt, err := strconv.Atoi(sig)
	if err != nil {
		httphelper.ValidationError(w, "sig", "must be an integer")
		return
	}
	id := ps.ByName("id")
	if err := h.host.SignalJob(id, sigInt); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *jobAPI) PullImages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	log := h.host.log.New("fn", "PullImages")
	r.Body.Close()

	query := r.URL.Query()
	repo := query.Get("repository")
	if repo == "" {
		repo = "randy-girard/flynn"
	}

	info := make(chan *ct.ImagePullInfo)
	stream := sse.NewStream(w, info, nil)
	go stream.Serve()

	d := downloader.New(repo, h.host.vman, query.Get("version"), log)

	log.Info("pulling images from GitHub", "repo", repo, "version", query.Get("version"))
	if err := d.DownloadImages(query.Get("config-dir"), info); err != nil {
		log.Error("error pulling images", "err", err)
		stream.CloseWithError(err)
		return
	}

	stream.Wait()
}

func (h *jobAPI) PullBinariesAndConfig(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	log := h.host.log.New("fn", "PullBinariesAndConfig")
	r.Body.Close()

	query := r.URL.Query()
	repo := query.Get("repository")
	if repo == "" {
		repo = "randy-girard/flynn"
	}

	d := downloader.New(repo, h.host.vman, query.Get("version"), log)

	log.Info("downloading binaries from GitHub", "repo", repo, "version", query.Get("version"))
	paths, err := d.DownloadBinaries(query.Get("bin-dir"))
	if err != nil {
		log.Error("error downloading binaries", "err", err)
		httphelper.Error(w, err)
		return
	}

	log.Info("downloading config from GitHub")
	configs, err := d.DownloadConfig(query.Get("config-dir"))
	if err != nil {
		log.Error("error downloading config", "err", err)
		httphelper.Error(w, err)
		return
	}
	for k, v := range configs {
		paths[k] = v
	}

	httphelper.JSON(w, 200, paths)
}

func (h *jobAPI) AddJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// TODO(titanous): validate UUID
	id := ps.ByName("id")

	log := h.host.log.New("fn", "AddJob", "job.id", id)

	if !h.addJobRateLimitBucket.Take() {
		log.Warn("maximum concurrent AddJob calls running")
		httphelper.Error(w, httphelper.JSONError{
			Code:    httphelper.RatelimitedErrorCode,
			Message: "maximum concurrent AddJob calls running, try again later",
			Retry:   true,
		})
		return
	}

	if shutdown.IsActive() {
		log.Warn("refusing to start job due to active shutdown")
		httphelper.JSON(w, 500, struct{}{})
		h.addJobRateLimitBucket.Put()
		return
	}

	log.Info("decoding job")
	job := &host.Job{ID: id}
	if err := httphelper.DecodeJSON(r, job); err != nil {
		log.Error("error decoding job", "err", err)
		httphelper.Error(w, err)
		h.addJobRateLimitBucket.Put()
		return
	}
	// SEC-008: reject HostNetwork/HostPIDNamespace unless the job is a system job
	if job.Config.HostNetwork && job.Metadata["flynn-controller.type"] != "system" {
		log.Warn("rejecting non-system job requesting host network")
		httphelper.ValidationError(w, "host_network", "only allowed for system jobs")
		h.addJobRateLimitBucket.Put()
		return
	}
	if job.Config.HostPIDNamespace && job.Metadata["flynn-controller.type"] != "system" {
		log.Warn("rejecting non-system job requesting host PID namespace")
		httphelper.ValidationError(w, "host_pid_namespace", "only allowed for system jobs")
		h.addJobRateLimitBucket.Put()
		return
	}

	if len(job.Mountspecs) == 0 {
		log.Warn("rejecting job as no mountspecs set")
		httphelper.ValidationError(w, "mountspecs", "must be set")
		h.addJobRateLimitBucket.Put()
		return
	}

	log.Info("acquiring state database")
	if err := h.host.state.Acquire(); err != nil {
		log.Error("error acquiring state database", "err", err)
		httphelper.Error(w, err)
		h.addJobRateLimitBucket.Put()
		return
	}

	if err := h.host.state.AddJob(job); err != nil {
		log.Error("error adding job to state database", "err", err)
		if err == ErrJobExists {
			httphelper.ConflictError(w, err.Error())
		} else {
			httphelper.Error(w, err)
		}
		h.addJobRateLimitBucket.Put()
		return
	}

	go func() {
		log.Info("running job")
		err := h.host.backend.Run(job, nil, h.addJobRateLimitBucket)
		h.host.state.Release()
		if err != nil {
			log.Error("error running job", "err", err)
			h.host.state.SetStatusFailed(job.ID, err)
		}
		h.addJobRateLimitBucket.Put()
	}()

	// TODO(titanous): return 201 Accepted
	httphelper.JSON(w, 200, struct{}{})
}

func (h *jobAPI) ConfigureDiscoverd(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "ConfigureDiscoverd")

	log.Info("decoding config")
	config := &host.DiscoverdConfig{}
	if err := httphelper.DecodeJSON(r, config); err != nil {
		log.Error("error decoding config", "err", err)
		httphelper.Error(w, err)
		return
	}
	log.Info("config decoded", "url", config.URL, "dns", config.DNS)

	h.host.ConfigureDiscoverd(config)
}

func (h *jobAPI) ConfigureNetworking(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "ConfigureNetworking")

	log.Info("decoding config")
	config := &host.NetworkConfig{}
	if err := httphelper.DecodeJSON(r, config); err != nil {
		log.Error("error decoding config", "err", err)
		shutdown.Fatal(err)
	}

	// configure the network before returning a response in case the
	// network coordinator requires the bridge to be created (e.g.
	// when using flannel with the "alloc" backend)
	h.host.ConfigureNetworking(config)
}

func (h *jobAPI) GetStatus(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	h.host.statusMtx.RLock()
	defer h.host.statusMtx.RUnlock()
	httphelper.JSON(w, 200, &h.host.status)
}

// GetJobStats returns runtime resource usage stats for a specific job/container.
func (h *jobAPI) GetJobStats(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id := ps.ByName("id")
	log := h.host.log.New("fn", "GetJobStats", "job.id", id)

	stats, err := h.host.backend.GetJobStats(id)
	if err != nil {
		log.Error("error getting job stats", "err", err)
		httphelper.ObjectNotFoundError(w, err.Error())
		return
	}

	httphelper.JSON(w, 200, stats)
}

// GetAllJobsStats returns runtime resource usage stats for all jobs on this host.
func (h *jobAPI) GetAllJobsStats(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "GetAllJobsStats")

	stats, err := h.host.backend.GetAllJobsStats()
	if err != nil {
		log.Error("error getting all jobs stats", "err", err)
		httphelper.Error(w, err)
		return
	}

	httphelper.JSON(w, 200, stats)
}

// GetHostStats returns aggregated resource usage stats for the host.
func (h *jobAPI) GetHostStats(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "GetHostStats")

	stats, err := h.host.backend.GetHostStats()
	if err != nil {
		log.Error("error getting host stats", "err", err)
		httphelper.Error(w, err)
		return
	}

	httphelper.JSON(w, 200, stats)
}

func (h *jobAPI) UpdateTags(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var tags map[string]string
	if err := httphelper.DecodeJSON(r, &tags); err != nil {
		httphelper.Error(w, err)
		return
	}
	if err := h.host.UpdateTags(tags); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *Host) UpdateTags(tags map[string]string) error {
	h.statusMtx.RLock()
	defer h.statusMtx.RUnlock()
	if err := h.discMan.UpdateTags(tags); err != nil {
		return err
	}
	h.status.Tags = tags
	return nil
}

func checkPort(port host.Port) bool {
	l, err := net.Listen(port.Proto, fmt.Sprintf(":%d", port.Port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func (h *jobAPI) ResourceCheck(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var req host.ResourceCheck
	if err := httphelper.DecodeJSON(r, &req); err != nil {
		httphelper.Error(w, err)
		return
	}
	var conflicts []host.Port
	for _, p := range req.Ports {
		if p.Proto == "" {
			p.Proto = "tcp"
		}
		if !checkPort(p) {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		resp := host.ResourceCheck{Ports: conflicts}
		detail, err := json.Marshal(resp)
		if err != nil {
			httphelper.Error(w, err)
			return
		}
		httphelper.JSON(w, 409, &httphelper.JSONError{
			Code:    httphelper.ConflictErrorCode,
			Message: "Conflicting resources found",
			Detail:  detail,
		})
		return
	}
	httphelper.JSON(w, 200, struct{}{})
}

func (h *jobAPI) Update(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "Update")

	log.Info("decoding command")
	var cmd host.Command
	if err := httphelper.DecodeJSON(req, &cmd); err != nil {
		log.Error("error decoding command", "err", err)
		httphelper.Error(w, err)
		return
	}

	log.Info("updating host")
	err := h.host.Update(&cmd)
	if err != nil {
		httphelper.Error(w, err)
		return
	}

	// send an ok response and then shutdown after a short delay to give
	// the response chance to reach the client.
	httphelper.JSON(w, http.StatusOK, cmd)
	delay := time.Second
	if cmd.ShutdownDelay != nil {
		delay = *cmd.ShutdownDelay
	}
	log.Info(fmt.Sprintf("shutting down in %s", delay))
	time.AfterFunc(delay, func() {
		log.Info("exiting")
		os.Exit(0)
	})
}

func (h *jobAPI) RegisterRoutes(r *httprouter.Router) error {
	r.GET("/host/jobs", h.ListJobs)
	r.GET("/host/jobs/:id", h.GetJob)
	r.PUT("/host/jobs/:id", h.AddJob)
	r.DELETE("/host/jobs/:id", h.StopJob)
	r.PUT("/host/jobs/:id/discoverd-deregister", h.DiscoverdDeregisterJob)
	r.PUT("/host/jobs/:id/signal/:signal", h.SignalJob)
	r.GET("/host/jobs/:id/stats", h.GetJobStats)
	r.POST("/host/pull/images", h.PullImages)
	r.POST("/host/pull/binaries", h.PullBinariesAndConfig)
	r.POST("/host/discoverd", h.ConfigureDiscoverd)
	r.POST("/host/network", h.ConfigureNetworking)
	r.GET("/host/status", h.GetStatus)
	r.GET("/host/stats", h.GetHostStats)
	r.GET("/host/jobs-stats", h.GetAllJobsStats)
	r.POST("/host/resource-check", h.ResourceCheck)
	r.POST("/host/update", h.Update)
	r.POST("/host/tags", h.UpdateTags)
	return nil
}

func (h *Host) ServeHTTP() {
	r := httprouter.New()

	r.POST("/attach", newAttachHandler(h.state, h.backend, h.log).ServeHTTP)

	jobAPI := &jobAPI{
		host:                  h,
		addJobRateLimitBucket: NewRateLimitBucket(h.maxJobConcurrency),
	}
	jobAPI.RegisterRoutes(r)

	h.volAPI.RegisterRoutes(r)

	h.sman.RegisterRoutes(r)

	// SEC-017: apply rate limiting before auth to prevent brute-force attacks
	go http.Serve(h.listener, h.rateLimitMiddleware(h.authMiddleware(httphelper.ContextInjector("host", httphelper.NewRequestLogger(r)))))
}

func (h *Host) OpenDBs() error {
	if err := h.state.OpenDB(); err != nil {
		return err
	}
	if err := h.sman.OpenDB(); err != nil {
		return err
	}
	return h.vman.OpenDB()
}

func (h *Host) CloseDBs() error {
	if err := h.state.CloseDB(); err != nil {
		return err
	}
	if err := h.sman.CloseDB(); err != nil {
		return err
	}
	return h.vman.CloseDB()
}

func (h *Host) OpenLogs(buffers host.LogBuffers) error {
	return h.backend.OpenLogs(buffers)
}

func (h *Host) CloseLogs() (host.LogBuffers, error) {
	return h.backend.CloseLogs()
}

func (h *Host) Close() error {
	if h.listener != nil {
		return h.listener.Close()
	}
	return nil
}

func newHTTPListener(addr string) (net.Listener, error) {
	fdEnv := os.Getenv("FLYNN_HTTP_FD")
	if fdEnv == "" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		return keepalive.Listener(l), nil
	}
	fd, err := strconv.Atoi(fdEnv)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "http")
	defer file.Close()
	return net.FileListener(file)
}

// RateLimitBucket implements a Token Bucket using a buffered channel
type RateLimitBucket struct {
	ch chan struct{}
}

func NewRateLimitBucket(size uint64) *RateLimitBucket {
	return &RateLimitBucket{ch: make(chan struct{}, size)}
}

// Take attempts to take a token from the bucket, returning whether or not a
// token was taken
func (r *RateLimitBucket) Take() bool {
	select {
	case r.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Wait takes the next available token
func (r *RateLimitBucket) Wait() {
	r.ch <- struct{}{}
}

// Put returns a token to the bucket
func (r *RateLimitBucket) Put() {
	<-r.ch
}

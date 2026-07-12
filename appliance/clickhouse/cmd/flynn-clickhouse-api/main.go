package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/flynn/pkg/resource"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/julienschmidt/httprouter"
	"github.com/inconshreveable/log15"
)

const (
	// DefaultServiceName is used if FLYNN_CLICKHOUSE is empty.
	DefaultServiceName = "clickhouse"

	// DefaultAddr is the default bind address for the provisioning API.
	DefaultAddr = ":3000"

	// PasswordLength is the size of generated ClickHouse passwords.
	PasswordLength = 32
)

func main() {
	m := NewMain()
	if err := m.ParseFlags(os.Args[1:]); err != nil {
		shutdown.Fatal(err)
	}
	if err := m.Run(); err != nil {
		shutdown.Fatal(err)
	}
	<-(chan struct{})(nil)
}

// Main represents the main program.
type Main struct {
	ln net.Listener

	ServiceName string
	Addr        string
	Handler     *Handler
	Logger      log15.Logger
}

// NewMain returns a new instance of Main.
func NewMain() *Main {
	return &Main{
		ServiceName: DefaultServiceName,
		Addr:        DefaultAddr,
		Handler:     NewHandler(),
		Logger:      log15.New("app", "clickhouse-api"),
	}
}

// ParseFlags parses environment variables into the program configuration.
func (m *Main) ParseFlags(args []string) error {
	if s := os.Getenv("FLYNN_CLICKHOUSE"); s != "" {
		m.ServiceName = s
	}
	if port := os.Getenv("PORT"); port != "" {
		m.Addr = ":" + port
	}
	m.Handler.ClickHouseImageID = os.Getenv("CLICKHOUSE_IMAGE_ID")
	m.Handler.Singleton = os.Getenv("SINGLETON") == "true"

	client, err := controller.NewClient("", os.Getenv("CONTROLLER_KEY"))
	if err != nil {
		m.Logger.Error("cannot connect to controller", "err", err)
		return err
	}
	m.Handler.ServiceName = m.ServiceName
	m.Handler.ControllerClient = client
	return nil
}

// Run executes the program.
func (m *Main) Run() error {
	ln, err := net.Listen("tcp", m.Addr)
	if err != nil {
		return err
	}
	m.ln = ln

	m.Handler.Logger = m.Logger.New("component", "http")

	hb, err := discoverd.AddServiceAndRegister(m.ServiceName+"-api", m.Addr)
	if err != nil {
		return err
	}
	shutdown.BeforeExit(func() { hb.Close() })

	h := httphelper.ContextInjector(m.ServiceName+"-api", httphelper.NewRequestLogger(m.Handler))
	go func() { http.Serve(ln, h) }()
	return nil
}

// Handler represents the provisioning HTTP handler.
type Handler struct {
	router *httprouter.Router

	ServiceName       string
	ControllerClient  controller.Client
	ClickHouseImageID string
	Singleton         bool
	Logger            log15.Logger
}

// NewHandler returns a new instance of Handler.
func NewHandler() *Handler {
	h := &Handler{
		router: httprouter.New(),
		Logger: log15.New(),
	}
	h.router.POST("/clusters", h.servePostCluster)
	h.router.DELETE("/clusters", h.serveDeleteCluster)
	h.router.GET("/ping", h.serveGetPing)
	return h
}

// ServeHTTP serves an HTTP request and returns a response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) { h.router.ServeHTTP(w, req) }

func (h *Handler) servePostCluster(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	serviceName := "clickhouse-" + random.UUID()
	password := random.String(PasswordLength)

	replicaCount := 3
	if h.Singleton {
		replicaCount = 1
	}

	env := map[string]string{
		"FLYNN_CLICKHOUSE":         serviceName,
		"CLICKHOUSE_PASSWORD":      password,
		"CLICKHOUSE_CLUSTER":       "flynn",
		"CLICKHOUSE_REPLICA_COUNT": strconv.Itoa(replicaCount),
		"SINGLETON":                strconv.FormatBool(h.Singleton),
	}

	release := &ct.Release{
		ArtifactIDs: []string{h.ClickHouseImageID},
		Meta:        make(map[string]string),
		Processes: map[string]ct.ProcessType{
			"keeper": {
				Ports: []ct.Port{
					{Port: 9181, Proto: "tcp"},
					{Port: 9234, Proto: "tcp"},
				},
				Volumes: []ct.VolumeReq{{Path: "/data"}},
				Args:    []string{"/bin/start-flynn-clickhouse", "keeper"},
				Service: serviceName + "-keeper",
			},
			"clickhouse": {
				Ports: []ct.Port{
					{Port: 8123, Proto: "tcp"},
					{Port: 9000, Proto: "tcp"},
					{Port: 9009, Proto: "tcp"},
					{Port: 9090, Proto: "tcp"},
				},
				Volumes: []ct.VolumeReq{{Path: "/data"}},
				Args:    []string{"/bin/start-flynn-clickhouse", "clickhouse"},
				Service: serviceName,
			},
		},
		Env: env,
	}

	app := &ct.App{
		Name: serviceName,
		Meta: map[string]string{"flynn-system-app": "true"},
	}
	if err := h.ControllerClient.CreateApp(app); err != nil {
		h.Logger.Error("error creating app", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.Logger.Info("creating release", "artifact.id", h.ClickHouseImageID)
	if err := h.ControllerClient.CreateRelease(app.ID, release); err != nil {
		h.Logger.Error("error creating release", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.Logger.Info("scaling formation", "release.id", release.ID, "replicas", replicaCount)
	timeout := 10 * time.Minute
	scaleOpts := ct.ScaleOptions{
		Processes: map[string]int{
			"keeper":     replicaCount,
			"clickhouse": replicaCount,
		},
		Timeout: &timeout,
	}
	if err := h.ControllerClient.ScaleAppRelease(app.ID, release.ID, scaleOpts); err != nil {
		h.Logger.Error("error deploying release", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.Logger.Info("setting app release", "release.id", release.ID)
	if err := h.ControllerClient.SetAppRelease(app.ID, release.ID); err != nil {
		h.Logger.Error("error setting app release", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.Logger.Info("waiting for clickhouse keeper to start", "service", serviceName+"-keeper")
	if _, err := discoverd.GetInstances(serviceName+"-keeper", 10*time.Minute); err != nil {
		h.Logger.Error("error waiting for clickhouse keeper to start", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.Logger.Info("waiting for clickhouse to start", "service", serviceName)
	if _, err := discoverd.GetInstances(serviceName, 10*time.Minute); err != nil {
		h.Logger.Error("error waiting for clickhouse to start", "err", err)
		httphelper.Error(w, err)
		return
	}

	host := "leader." + serviceName + ".discoverd"
	nativeURL := &url.URL{
		Scheme: "clickhouse",
		Host:   net.JoinHostPort(host, "9000"),
		User:   url.UserPassword("default", password),
	}
	httpURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, "8123"),
		User:   url.UserPassword("default", password),
	}

	httphelper.JSON(w, 200, resource.Resource{
		ID: fmt.Sprintf("/clusters/%s", release.ID),
		Env: map[string]string{
			"FLYNN_CLICKHOUSE":         app.Name,
			"CLICKHOUSE_URL":           nativeURL.String(),
			"CLICKHOUSE_HTTP_URL":      httpURL.String(),
			"CLICKHOUSE_HOST":          host,
			"CLICKHOUSE_PORT":          "9000",
			"CLICKHOUSE_HTTP_PORT":     "8123",
			"CLICKHOUSE_USER":          "default",
			"CLICKHOUSE_PASSWORD":      password,
			"CLICKHOUSE_DATABASE":      "default",
			"CLICKHOUSE_CLUSTER":       "flynn",
			"CLICKHOUSE_REPLICA_COUNT": strconv.Itoa(replicaCount),
		},
	})
}

func (h *Handler) serveDeleteCluster(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	releaseID := strings.TrimPrefix(req.FormValue("id"), "/clusters/")
	if releaseID == "" {
		httphelper.ValidationError(w, "id", "is invalid")
		return
	}

	release, err := h.ControllerClient.GetRelease(releaseID)
	if err != nil {
		h.Logger.Error("error finding release", "err", err, "release.id", releaseID)
		httphelper.Error(w, err)
		return
	}

	appName := release.Env["FLYNN_CLICKHOUSE"]
	if appName == "" {
		httphelper.Error(w, errors.New("unable to find app name"))
		return
	}

	h.Logger.Info("destroying app", "app.name", appName)
	if _, err := h.ControllerClient.DeleteApp(appName); err != nil {
		h.Logger.Error("error destroying app", "err", err)
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *Handler) serveGetPing(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	w.WriteHeader(200)
}

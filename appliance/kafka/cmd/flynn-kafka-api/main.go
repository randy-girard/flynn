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

	"github.com/flynn/flynn/appliance/kafka"
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
	// DefaultServiceName is used if FLYNN_KAFKA is empty.
	DefaultServiceName = "kafka"

	// DefaultAddr is the default bind address for the provisioning API.
	DefaultAddr = ":3000"

	// ClusterIDBytes is the number of random bytes in a KRaft cluster id.
	ClusterIDBytes = 16
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
		Logger:      log15.New("app", "kafka-api"),
	}
}

// ParseFlags parses environment variables into the program configuration.
func (m *Main) ParseFlags(args []string) error {
	if s := os.Getenv("FLYNN_KAFKA"); s != "" {
		m.ServiceName = s
	}
	if port := os.Getenv("PORT"); port != "" {
		m.Addr = ":" + port
	}
	m.Handler.KafkaImageID = os.Getenv("KAFKA_IMAGE_ID")
	m.Handler.Singleton = os.Getenv("SINGLETON") == "true"
	// TLS is enabled by default; operators may disable it cluster-wide by
	// setting KAFKA_TLS_ENABLED=false on the kafka system app.
	m.Handler.TLSEnabled = os.Getenv("KAFKA_TLS_ENABLED") != "false"

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

	ServiceName      string
	ControllerClient controller.Client
	KafkaImageID     string
	Singleton        bool
	TLSEnabled       bool
	Logger           log15.Logger
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
	// Each provisioned cluster gets its own app, service name & cluster id.
	serviceName := "kafka-" + random.UUID()
	clusterID := random.Base64(ClusterIDBytes)

	brokerCount := 3
	if h.Singleton {
		brokerCount = 1
	}
	replication := brokerCount
	if replication > 3 {
		replication = 3
	}
	minISR := replication - 1
	if minISR < 1 {
		minISR = 1
	}

	env := map[string]string{
		"FLYNN_KAFKA":              serviceName,
		"KAFKA_CLUSTER_ID":         clusterID,
		"KAFKA_BROKER_COUNT":       strconv.Itoa(brokerCount),
		"KAFKA_REPLICATION_FACTOR": strconv.Itoa(replication),
		"KAFKA_MIN_ISR":            strconv.Itoa(minISR),
		"SINGLETON":                strconv.FormatBool(h.Singleton),
	}

	// When TLS is enabled, mint a private CA plus broker and client
	// certificates for the CLIENT listener's mutual TLS. Inter-broker and
	// controller traffic stays on the internal PLAINTEXT listeners.
	var tls *kafka.TLSBundle
	if h.TLSEnabled {
		hosts := []string{
			"leader." + serviceName + ".discoverd",
			serviceName + ".discoverd",
			"localhost",
			"127.0.0.1",
		}
		var err error
		tls, err = kafka.GenerateTLSBundle(hosts)
		if err != nil {
			h.Logger.Error("error generating tls bundle", "err", err)
			httphelper.Error(w, err)
			return
		}
		env["KAFKA_TLS_ENABLED"] = "true"
		env["KAFKA_TRUSTED_CERT"] = tls.CACert
		env["KAFKA_TLS_SERVER_CERT"] = tls.ServerCert
		env["KAFKA_TLS_SERVER_KEY"] = tls.ServerKey
		env["KAFKA_CLIENT_CERT"] = tls.ClientCert
		env["KAFKA_CLIENT_CERT_KEY"] = tls.ClientKey
	}

	release := &ct.Release{
		ArtifactIDs: []string{h.KafkaImageID},
		Meta:        make(map[string]string),
		Processes: map[string]ct.ProcessType{
			"kafka": {
				Ports: []ct.Port{
					{Port: 9092, Proto: "tcp"},
					{Port: 9093, Proto: "tcp"},
					{Port: 9094, Proto: "tcp"},
					{Port: 9095, Proto: "tcp"},
				},
				Volumes: []ct.VolumeReq{{Path: "/data"}},
				Args:    []string{"/bin/start-flynn-kafka", "kafka"},
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

	h.Logger.Info("creating release", "artifact.id", h.KafkaImageID)
	if err := h.ControllerClient.CreateRelease(app.ID, release); err != nil {
		h.Logger.Error("error creating release", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.Logger.Info("scaling formation", "release.id", release.ID, "brokers", brokerCount)
	timeout := 10 * time.Minute
	scaleOpts := ct.ScaleOptions{Processes: map[string]int{"kafka": brokerCount}, Timeout: &timeout}
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

	h.Logger.Info("waiting for kafka to start", "service", serviceName)
	if _, err := discoverd.GetInstances(serviceName, 10*time.Minute); err != nil {
		h.Logger.Error("error waiting for kafka to start", "err", err)
		httphelper.Error(w, err)
		return
	}

	host := "leader." + serviceName + ".discoverd"
	bootstrap := host + ":9092"

	scheme := "kafka"
	if h.TLSEnabled {
		scheme = "kafka+ssl"
	}
	u := url.URL{Scheme: scheme, Host: bootstrap}

	appEnv := map[string]string{
		"FLYNN_KAFKA":             app.Name,
		"KAFKA_URL":               u.String(),
		"KAFKA_BROKER_URLS":       u.String(),
		"KAFKA_BOOTSTRAP_SERVERS": bootstrap,
		"KAFKA_HOST":              host,
		"KAFKA_PORT":              "9092",
	}
	if h.TLSEnabled && tls != nil {
		appEnv["KAFKA_TLS_ENABLED"] = "true"
		appEnv["KAFKA_TRUSTED_CERT"] = tls.CACert
		appEnv["KAFKA_CLIENT_CERT"] = tls.ClientCert
		appEnv["KAFKA_CLIENT_CERT_KEY"] = tls.ClientKey
	}

	httphelper.JSON(w, 200, resource.Resource{
		ID:  fmt.Sprintf("/clusters/%s", release.ID),
		Env: appEnv,
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

	appName := release.Env["FLYNN_KAFKA"]
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

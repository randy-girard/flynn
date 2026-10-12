package main

import (
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/appliance/postgresql"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/shutdown"
	sd "github.com/randy-girard/flynn/pkg/sirenia/discoverd"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func main() {
	serviceName := os.Getenv("FLYNN_POSTGRES")
	if serviceName == "" {
		serviceName = "postgres"
	}
	singleton := os.Getenv("SINGLETON") == "true"
	password := os.Getenv("PGPASSWORD")
	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "5433"
	}

	const dataDir = "/data"
	idFile := filepath.Join(dataDir, "instance_id")
	idBytes, err := ioutil.ReadFile(idFile)
	if err != nil && !os.IsNotExist(err) {
		shutdown.Fatalf("error reading instance ID: %s", err)
	}
	id := string(idBytes)
	if len(id) == 0 {
		id = random.UUID()
		if err := ioutil.WriteFile(idFile, []byte(id), 0644); err != nil {
			shutdown.Fatalf("error writing instance ID: %s", err)
		}
	}

	err = discoverd.DefaultClient.AddService(serviceName, &discoverd.ServiceConfig{
		LeaderType: discoverd.LeaderTypeManual,
	})
	if err != nil && !httphelper.IsObjectExistsError(err) {
		shutdown.Fatal(err)
	}
	inst := &discoverd.Instance{
		Addr: ":5432",
		Meta: map[string]string{postgresql.IDKey: id},
	}
	hb, err := discoverd.DefaultClient.RegisterInstance(serviceName, inst)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { hb.Close() })

	log := log15.New("app", "postgres")

	process := postgresql.NewProcess(postgresql.Config{
		ID:           id,
		Singleton:    singleton,
		DataDir:      filepath.Join(dataDir, "db"),
		BinDir:       "/usr/lib/postgresql/16/bin/",
		Password:     password,
		Logger:       log.New("component", "postgres"),
		TimescaleDB:  true,
		ExtWhitelist: true,
		WaitUpstream: true,
		SHMType:      "posix",
		TLSHosts:     postgresTLSHosts(serviceName),
	})
	dd := sd.NewDiscoverd(discoverd.DefaultClient.Service(serviceName), log.New("component", "discoverd"))

	peer := state.NewPeer(inst, id, postgresql.IDKey, singleton, dd, process, log.New("component", "peer"))
	shutdown.BeforeExit(func() { _ = process.Stop() })
	shutdown.BeforeExit(func() { peer.Close() })

	go peer.Run()

	handler := postgresql.NewHandler()
	handler.Process = process
	handler.Peer = peer
	handler.Heartbeater = hb
	handler.Logger = log.New("component", "http")

	if port := os.Getenv(host.MetricsPortEnv); port != "" {
		go func() {
			srv := &http.Server{
				Addr:              ":" + port,
				Handler:           postgresql.MetricsHandler(serviceName, process.MetricsSeries),
				ReadHeaderTimeout: 5 * time.Second,
			}
			log.Error("metrics listener stopped", "addr", srv.Addr, "err", srv.ListenAndServe())
		}()
	}

	shutdown.Fatal(http.ListenAndServe(":"+httpPort, handler))
}

func postgresTLSHosts(serviceName string) []string {
	if serviceName == "" {
		serviceName = "postgres"
	}
	hosts := []string{
		serviceName + ".discoverd",
		"leader." + serviceName + ".discoverd",
		serviceName,
		"localhost",
	}
	for _, env := range []string{"DEFAULT_ROUTE_DOMAIN", "CLUSTER_DOMAIN"} {
		if d := strings.TrimSpace(os.Getenv(env)); d != "" {
			hosts = append(hosts, serviceName+"."+d, "*."+d, d)
			break
		}
	}
	return hosts
}

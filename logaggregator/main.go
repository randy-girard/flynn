package main

import (
	"net"
	"os"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/appliancehttp"
	"github.com/randy-girard/flynn/pkg/shutdown"

	"github.com/inconshreveable/log15"
)

func main() {
	defer shutdown.Exit()

	apiPort := os.Getenv("PORT_0")
	if apiPort == "" {
		apiPort = "5000"
	}

	logPort := os.Getenv("PORT_1")
	if logPort == "" {
		logPort = "3000"
	}

	serviceName := os.Getenv("SERVICE_NAME")
	if serviceName == "" {
		serviceName = "logaggregator"
	}

	keys := appliancehttp.Keys()
	if len(keys) == 0 {
		log15.Error("CONTROLLER_KEY is not set: the log API will refuse every request")
	}
	ttl := parseBufferTTL(os.Getenv("LOG_BUFFER_TTL"))
	conf := ServerConfig{
		SyslogAddr:  ":" + logPort,
		ApiAddr:     ":" + apiPort,
		Discoverd:   discoverd.DefaultClient,
		ServiceName: serviceName,
		APIKeys:     keys,
		BufferTTL:   ttl,
	}
	if len(keys) > 0 {
		// app deletions drop that app's log buffer
		cc, err := controller.NewClient("", appliancehttp.ClientKey())
		if err != nil {
			log15.Error("error creating controller client, deleted apps' logs are only dropped by LOG_BUFFER_TTL", "err", err)
		} else {
			conf.AppEvents = cc
		}
	}

	srv := NewServer(conf)
	shutdown.BeforeExit(srv.Shutdown)

	// load the running instances' buffered logs (leader first) before
	// registering; bounded, see loadSnapshotFromPeers
	if sources := snapshotSources(conf.Discoverd.Service(conf.ServiceName)); len(sources) > 0 {
		srv.loadSnapshotFromPeers(snapshotLoader{
			Sources:       sources,
			Key:           appliancehttp.ClientKey(),
			HTTP:          snapshotHTTPClient(defaultSnapshotDial),
			IdleTimeout:   defaultSnapshotIdle,
			SourceTimeout: defaultSnapshotSource,
			TotalTimeout:  defaultSnapshotTotal,
		})
	}

	if err := srv.Start(); err != nil {
		shutdown.Fatal(err)
	}
	<-make(chan struct{})
}

// snapshotSources returns the API URLs of the registered instances, leader
// first. Instances register their syslog address; the API is on port 80 of
// the same host.
func snapshotSources(svc discoverd.Service) []string {
	var hosts []string
	seen := make(map[string]bool)
	add := func(addr string) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || host == "" || seen[host] {
			return
		}
		seen[host] = true
		hosts = append(hosts, "http://"+host)
	}
	if leader, err := svc.Leader(); err == nil {
		add(leader.Addr)
	} else {
		log15.Info("error finding leader for snapshot", "error", err)
	}
	if insts, err := svc.Instances(); err == nil {
		for _, inst := range insts {
			add(inst.Addr)
		}
	}
	if len(hosts) > 0 {
		log15.Info("loading snapshot", "sources", hosts)
	}
	return hosts
}

// parseBufferTTL reads LOG_BUFFER_TTL (a Go duration such as 72h). Empty or
// invalid values give DefaultBufferTTL.
func parseBufferTTL(v string) time.Duration {
	if v == "" {
		return DefaultBufferTTL
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log15.Error("invalid LOG_BUFFER_TTL, using the default", "value", v, "default", DefaultBufferTTL)
		return DefaultBufferTTL
	}
	return d
}

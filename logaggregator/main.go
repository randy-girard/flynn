package main

import (
	"net"
	"os"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/logaggregator/client"
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

	// get leader for snapshot (if any)
	leader, err := conf.Discoverd.Service(conf.ServiceName).Leader()
	if err == nil {
		host, _, _ := net.SplitHostPort(leader.Addr)
		log15.Info("loading snapshot from leader", "leader", host)

		c, _ := client.New("http://" + host)
		snapshot, err := c.GetSnapshot()
		if err == nil {
			if err := srv.LoadSnapshot(snapshot); err != nil {
				log15.Error("error receiving snapshot from leader", "error", err)
			}
			snapshot.Close()
		} else {
			log15.Error("error getting snapshot from leader", "error", err)
		}
	} else {
		log15.Info("error finding leader for snapshot", "error", err)
	}

	if err := srv.Start(); err != nil {
		shutdown.Fatal(err)
	}
	<-make(chan struct{})
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

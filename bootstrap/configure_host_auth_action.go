package bootstrap

import (
	"fmt"
	"os"
	"time"

	"github.com/flynn/flynn/pkg/cluster"
)

type ConfigureHostAuthAction struct{}

func init() {
	Register("configure-host-auth", &ConfigureHostAuthAction{})
}

func (a *ConfigureHostAuthAction) Run(s *State) error {
	data, ok := s.StepData["host-key"].(*RandomData)
	if !ok || data.Data == "" {
		return fmt.Errorf("bootstrap: host-key step data missing")
	}
	key := data.Data

	clientKey := os.Getenv("FLYNN_HOST_AUTH_KEY")
	for _, h := range s.Hosts {
		client := cluster.NewHostWithKey(h.ID(), h.Addr(), nil, h.Tags(), clientKey)
		err := client.ConfigureAuthKey(key)
		if err != nil && clientKey != "" {
			client = cluster.NewHostWithKey(h.ID(), h.Addr(), nil, h.Tags(), "")
			err = client.ConfigureAuthKey(key)
		}
		if err != nil {
			return fmt.Errorf("bootstrap: error configuring host auth on %s: %s", h.Addr(), err)
		}
	}
	s.SetHostAuthKey(key)
	os.Setenv("FLYNN_HOST_AUTH_KEY", key)

	// ConfigureAuthKey schedules an asynchronous daemon restart, so the
	// pre-restart daemon (which still has auth disabled) keeps answering the
	// auth-exempt /host/status endpoint for a short window. Wait until every
	// host reports auth is enabled, which only happens once the daemon has
	// actually restarted with the new key; otherwise later actions open job
	// event streams against a daemon that is about to be restarted out from
	// under them and time out waiting for events.
	if err := waitForHostAuth(s); err != nil {
		return err
	}
	s.refreshHostClients()
	return nil
}

// waitForHostAuth blocks until every host in the bootstrap state reports that
// auth is enabled (HostStatus.Auth), which only happens once the daemon has
// actually restarted with the new key. It returns an error if s.HostTimeout
// elapses before all hosts are ready.
func waitForHostAuth(s *State) error {
	const waitInterval = 500 * time.Millisecond
	timeout := time.After(s.HostTimeout)
	for {
		ready := 0
		for _, h := range s.Hosts {
			client := s.HostClient(h.ID(), h.Addr(), h.Tags())
			status, err := client.GetStatus()
			if err != nil || !status.Auth {
				continue
			}
			ready++
		}
		if ready >= len(s.Hosts) {
			return nil
		}
		select {
		case <-timeout:
			return fmt.Errorf("bootstrap: timed out waiting for hosts to restart after enabling auth")
		case <-time.After(waitInterval):
		}
	}
}

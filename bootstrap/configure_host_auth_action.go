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

	const waitInterval = 500 * time.Millisecond
	timeout := time.After(s.HostTimeout)
	for {
		ready := 0
		for _, h := range s.Hosts {
			client := s.HostClient(h.ID(), h.Addr(), h.Tags())
			if _, err := client.GetStatus(); err != nil {
				continue
			}
			ready++
		}
		if ready >= len(s.Hosts) {
			s.refreshHostClients()
			return nil
		}
		select {
		case <-timeout:
			return fmt.Errorf("bootstrap: timed out waiting for hosts to restart after enabling auth")
		case <-time.After(waitInterval):
		}
	}
}

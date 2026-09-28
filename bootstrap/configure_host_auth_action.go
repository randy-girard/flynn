package bootstrap

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/randy-girard/flynn/pkg/cluster"
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

	extra := map[string]string{}
	if dkey := s.DiscoverdAuthKey(); dkey != "" {
		extra["DISCOVERD_AUTH_KEY"] = dkey
		os.Setenv("DISCOVERD_AUTH_KEY", dkey)
	}
	ck := s.controllerKey
	if ck == "" {
		if d, ok := s.StepData["controller-key"].(*RandomData); ok {
			ck = d.Data
		}
	}
	if ck != "" {
		extra["AUTH_KEY"] = ck
		extra["CONTROLLER_KEY"] = ck
		os.Setenv("AUTH_KEY", ck)
		os.Setenv("CONTROLLER_KEY", ck)
		s.SetControllerKey(ck)
	}
	if d := discoverdEnv(s); d != "" {
		extra["DISCOVERD"] = d
		os.Setenv("DISCOVERD", d)
	}

	clientKey := os.Getenv("FLYNN_HOST_AUTH_KEY")
	for _, h := range s.Hosts {
		if err := configureHostAuthOn(h, key, clientKey, extra); err != nil {
			return fmt.Errorf("bootstrap: error configuring host auth on %s: %s", h.Addr(), err)
		}
	}
	s.SetHostAuthKey(key)
	os.Setenv("FLYNN_HOST_AUTH_KEY", key)

	// systemd-managed hosts schedule an asynchronous daemon restart, so
	// the pre-restart daemon keeps answering the auth-exempt GET
	// /host/status endpoint with Auth=false until it is replaced. Wait
	// until every host reports Auth. start-flynn-host / vagrant-dev apply
	// the key in-process instead, so this returns as soon as status flips.
	if err := waitForHostAuth(s); err != nil {
		return err
	}
	s.refreshHostClients()
	return nil
}

func configureHostAuthOn(h *cluster.Host, key, clientKey string, extra map[string]string) error {
	var last error
	for _, addr := range configureAuthDialAddrs(h.Addr()) {
		client := cluster.NewHostWithKey(h.ID(), addr, nil, h.Tags(), clientKey)
		err := client.ConfigureHostSecrets(key, extra)
		if err != nil && clientKey != "" {
			client = cluster.NewHostWithKey(h.ID(), addr, nil, h.Tags(), "")
			err = client.ConfigureHostSecrets(key, extra)
		}
		if err == nil {
			return nil
		}
		last = err
	}
	return last
}

// configureAuthDialAddrs prefers 127.0.0.1 when addr is this machine
// (faster on-node TOFU). Remote advertised IPs are unchanged; empty-key
// POST /host/auth-key remains allowed on those addresses for multi-node
// bootstrap.
func configureAuthDialAddrs(addr string) []string {
	if loop := loopbackAuthAddr(addr); loop != "" && loop != addr {
		return []string{loop, addr}
	}
	return []string{addr}
}

func loopbackAuthAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return addr
	}
	if !hostIPIsLocalInterface(ip) {
		return ""
	}
	return net.JoinHostPort("127.0.0.1", port)
}

func hostIPIsLocalInterface(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var ifaceIP net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ifaceIP = v.IP
		case *net.IPAddr:
			ifaceIP = v.IP
		}
		if ifaceIP != nil && ifaceIP.Equal(ip) {
			return true
		}
	}
	return false
}

// discoverdEnv is the DISCOVERD URL list to persist in host.json so the
// flynn-host CLI does not fall back to 127.0.0.1:1111. bootstrap-flynn and
// vagrant-dev bind discoverd on the TEST-NET listen IP (192.0.2.200), not
// loopback.
func discoverdEnv(s *State) string {
	if d := strings.TrimSpace(os.Getenv("DISCOVERD")); d != "" && d != "none" {
		return d
	}
	if s == nil {
		return ""
	}
	ips := s.SortedHostIPs()
	parts := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		parts = append(parts, net.JoinHostPort(ip, "1111"))
	}
	return strings.Join(parts, ",")
}

// waitForHostAuth blocks until every host in the bootstrap state reports that
// auth is enabled (HostStatus.Auth): after a systemd restart, or immediately
// when the daemon applied the key in-process. It returns an error if
// s.HostTimeout elapses before all hosts are ready.
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

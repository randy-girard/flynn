package fixer

import (
	"fmt"
	"net"
	"strings"

	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
)

// FixHostBackend re-applies persisted network/discoverd configuration on each
// host when flynn-host restarts without re-notifying the daemon. Jobs added
// after a restart block in Run() until networkConfigured and
// discoverdConfigured are closed; that only happens via ConfigureNetworking
// and ConfigureDiscoverd. Persisted global state often has an empty job_id,
// which previously caused restore to skip both calls entirely.
func (f *ClusterFixer) FixHostBackend() error {
	f.l.Info("ensuring host backends are configured for job scheduling")
	for _, h := range f.hosts {
		status, err := h.GetStatus()
		if err != nil {
			return fmt.Errorf("error getting status from %s: %s", h.ID(), err)
		}
		if status.Network == nil || status.Network.Subnet == "" {
			if err := f.ensureHostNetwork(h); err != nil {
				f.l.Error("error ensuring host network", "host", h.ID(), "err", err)
			}
		}
		if status.Discoverd == nil || status.Discoverd.URL == "" {
			if err := f.ensureHostDiscoverd(h); err != nil {
				f.l.Error("error ensuring host discoverd", "host", h.ID(), "err", err)
			}
			continue
		}
		// After a flannel subnet change, discoverd may keep listening on the
		// previous bridge address while persisted state already names the new
		// subnet DNS endpoint. Re-notify discoverd so container DNS works.
		if wantDNS, err := f.expectedDiscoverdDNS(h); err == nil && wantDNS != "" && status.Discoverd.DNS != wantDNS {
			f.l.Info("discoverd DNS mismatch, reconfiguring", "host", h.ID(), "have", status.Discoverd.DNS, "want", wantDNS)
			cfg := *status.Discoverd
			cfg.DNS = wantDNS
			if err := h.ConfigureDiscoverd(&cfg); err != nil {
				f.l.Error("error reconfiguring discoverd DNS", "host", h.ID(), "err", err)
			}
		}
		if wantURL := normalizeDiscoverdURL(status.Discoverd.URL); wantURL != "" && wantURL != status.Discoverd.URL {
			f.l.Info("discoverd URL missing scheme, reconfiguring", "host", h.ID(), "have", status.Discoverd.URL, "want", wantURL)
			cfg := *status.Discoverd
			cfg.URL = wantURL
			if err := h.ConfigureDiscoverd(&cfg); err != nil {
				f.l.Error("error reconfiguring discoverd URL", "host", h.ID(), "err", err)
			}
		}
	}
	return nil
}

func (f *ClusterFixer) ensureHostNetwork(h *cluster.Host) error {
	jobs, err := h.ListActiveJobs()
	if err != nil {
		return err
	}
	var flannelID string
	for _, j := range jobs {
		if j.Job.Metadata["flynn-controller.app_name"] == "flannel" && j.Job.Metadata["flynn-controller.type"] == "app" {
			flannelID = j.Job.ID
			break
		}
	}
	cfg, err := f.inferHostNetworkConfig(h)
	if err != nil {
		return err
	}
	cfg.JobID = flannelID
	f.l.Info("configuring host network", "host", h.ID(), "subnet", cfg.Subnet)
	return h.ConfigureNetworking(cfg)
}

func (f *ClusterFixer) inferHostNetworkConfig(h *cluster.Host) (*host.NetworkConfig, error) {
	jobs, err := h.ListJobs()
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.InternalIP == "" {
			continue
		}
		ip := net.ParseIP(j.InternalIP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		octets := strings.Split(ip.String(), ".")
		if len(octets) != 4 {
			continue
		}
		subnet := fmt.Sprintf("%s.%s.%s.1/24", octets[0], octets[1], octets[2])
		return &host.NetworkConfig{
			Subnet:    subnet,
			MTU:       1450,
			Resolvers: []string{"127.0.0.53"},
		}, nil
	}
	return nil, fmt.Errorf("unable to infer network config for %s", h.ID())
}

func (f *ClusterFixer) ensureHostDiscoverd(h *cluster.Host) error {
	jobs, err := h.ListActiveJobs()
	if err != nil {
		return err
	}
	var discoverdID string
	for _, j := range jobs {
		if j.Job.Metadata["flynn-controller.app_name"] == "discoverd" && j.Job.Metadata["flynn-controller.type"] == "app" {
			discoverdID = j.Job.ID
			break
		}
	}
	peers := make([]string, 0, len(f.hosts))
	for _, peer := range f.hosts {
		addr := peer.Addr()
		if addr == "" {
			continue
		}
		if i := strings.LastIndex(addr, ":"); i > 0 {
			addr = addr[:i]
		}
		peers = append(peers, fmt.Sprintf("http://%s:1111", addr))
	}
	if len(peers) == 0 {
		return fmt.Errorf("no discoverd peer URLs available")
	}
	cfg := &host.DiscoverdConfig{
		JobID: discoverdID,
		URL:   normalizeDiscoverdURL(strings.Join(peers, ",")),
	}
	if dns, err := f.expectedDiscoverdDNS(h); err == nil {
		cfg.DNS = dns
	}
	f.l.Info("configuring host discoverd", "host", h.ID(), "url", cfg.URL, "dns", cfg.DNS)
	return h.ConfigureDiscoverd(cfg)
}

func (f *ClusterFixer) expectedDiscoverdDNS(h *cluster.Host) (string, error) {
	network, err := f.inferHostNetworkConfig(h)
	if err != nil {
		return "", err
	}
	return dnsFromSubnet(network.Subnet)
}

func dnsFromSubnet(subnet string) (string, error) {
	parts := strings.Split(subnet, "/")
	if len(parts) != 2 || parts[0] == "" {
		return "", fmt.Errorf("invalid subnet %q", subnet)
	}
	return net.JoinHostPort(parts[0], "53"), nil
}

// normalizeDiscoverdURL ensures each discoverd peer URL has an http:// scheme.
// Persisted host state on some nodes omitted the scheme; clients add it at
// runtime but ConnectLocal stores the raw value in DISCOVERD container env.
func normalizeDiscoverdURL(url string) string {
	if url == "" {
		return ""
	}
	peers := strings.Split(url, ",")
	changed := false
	for i, peer := range peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		if !strings.Contains(peer, "://") {
			peer = "http://" + peer
			changed = true
		}
		peers[i] = peer
	}
	if !changed {
		return url
	}
	return strings.Join(peers, ",")
}

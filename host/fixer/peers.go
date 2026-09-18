package fixer

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

const hostHTTPPort = "1113"

func parsePeerIPList(list string) []string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	var ips []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(list, ",") {
		ip := strings.TrimSpace(part)
		if ip == "" {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		ips = append(ips, ip)
	}
	return ips
}

// localFixPeerIPs is 127.0.0.1 plus every non-link-local interface address.
// flynn-host often binds the advertise IP (not loopback), so loopback alone
// is not enough; when it binds 0.0.0.0, 127.0.0.1 works.
func localFixPeerIPs() []string {
	seen := map[string]struct{}{"127.0.0.1": {}}
	ips := []string{"127.0.0.1"}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		s := ip.String()
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		ips = append(ips, s)
	}
	return ips
}

func extraPeerIPsFromStatus(status *host.HostStatus) []string {
	if status == nil {
		return nil
	}
	var ips []string
	seen := map[string]struct{}{}
	addHostPort := func(hostport string) {
		hostport = strings.TrimSpace(hostport)
		if hostport == "" {
			return
		}
		host, _, err := net.SplitHostPort(hostport)
		if err != nil {
			host = hostport
		}
		host = strings.Trim(host, "[]")
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		ips = append(ips, host)
	}
	if status.URL != "" {
		if u, err := url.Parse(status.URL); err == nil && u.Host != "" {
			addHostPort(u.Host)
		}
	}
	if status.Discoverd != nil {
		for _, part := range strings.Split(status.Discoverd.URL, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !strings.Contains(part, "://") {
				part = "http://" + part
			}
			u, err := url.Parse(part)
			if err != nil || u.Host == "" {
				continue
			}
			addHostPort(u.Host)
		}
	}
	return ips
}

func (f *ClusterFixer) connectPeerIP(ip string) (*cluster.Host, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, fmt.Errorf("empty peer IP")
	}
	if f.ConnectPeerIP != nil {
		return f.ConnectPeerIP(ip)
	}
	u := "http://" + net.JoinHostPort(ip, hostHTTPPort)
	h := cluster.NewHost("", u, nil, nil)
	status, err := h.GetStatus()
	if err != nil {
		return nil, fmt.Errorf("error connecting to %s: %w", ip, err)
	}
	return cluster.NewHost(status.ID, u, nil, nil), nil
}

func (f *ClusterFixer) localPeerIPs() []string {
	if f.LocalPeerIPs != nil {
		return f.LocalPeerIPs()
	}
	return localFixPeerIPs()
}

func (f *ClusterFixer) hostStatus(h *cluster.Host) (*host.HostStatus, error) {
	if f.HostStatus != nil {
		return f.HostStatus(h)
	}
	return h.GetStatus()
}

// hostsWhenDiscoverdDown talks to flynn-host :1113 using --peer-ips, or the
// local daemon when that flag is omitted (typical singleton / --min-hosts 1).
func (f *ClusterFixer) hostsWhenDiscoverdDown(minHosts int, peerIPList string) ([]*cluster.Host, error) {
	candidates := parsePeerIPList(peerIPList)
	if len(candidates) == 0 {
		candidates = f.localPeerIPs()
		if f.l != nil {
			f.l.Info("discoverd unavailable, probing local flynn-host HTTP API", "candidates", strings.Join(candidates, ","))
		}
	}
	hosts, connected, lastErr := f.connectPeerIPs(candidates, nil)
	if len(hosts) == 0 {
		if peerIPList == "" {
			return nil, fmt.Errorf("error connecting to discoverd, local flynn-host on :%s also failed (pass --peer-ips with this host's IP): %v", hostHTTPPort, lastErr)
		}
		return nil, fmt.Errorf("error connecting to discoverd, use --peer-ips: %v", lastErr)
	}
	extra := make([]string, 0)
	for _, h := range hosts {
		status, err := f.hostStatus(h)
		if err != nil {
			continue
		}
		extra = append(extra, extraPeerIPsFromStatus(status)...)
	}
	hosts, _, _ = f.connectPeerIPs(extra, connected)
	if len(hosts) < minHosts {
		return nil, fmt.Errorf("number of reachable host APIs (%d) is less than --min-hosts (%d); pass --peer-ips", len(hosts), minHosts)
	}
	return hosts, nil
}

func (f *ClusterFixer) connectPeerIPs(ips []string, existing map[string]*cluster.Host) ([]*cluster.Host, map[string]*cluster.Host, error) {
	byID := existing
	if byID == nil {
		byID = map[string]*cluster.Host{}
	}
	seenIP := map[string]struct{}{}
	var lastErr error
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if _, ok := seenIP[ip]; ok {
			continue
		}
		seenIP[ip] = struct{}{}
		h, err := f.connectPeerIP(ip)
		if err != nil {
			lastErr = err
			if f.l != nil {
				f.l.Error("unable to reach flynn-host HTTP API", "ip", ip, "error", err)
			}
			continue
		}
		if h == nil || h.ID() == "" {
			continue
		}
		if _, ok := byID[h.ID()]; ok {
			continue
		}
		byID[h.ID()] = h
	}
	out := make([]*cluster.Host, 0, len(byID))
	for _, h := range byID {
		out = append(out, h)
	}
	return out, byID, lastErr
}

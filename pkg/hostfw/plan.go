package hostfw

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/randy-girard/flynn/pkg/instanceport"
	router "github.com/randy-girard/flynn/router/types"
)

const (
	KindPublic   = "public"
	KindCluster  = "cluster"
	KindPeer     = "peer"
	KindExpose   = "expose"
	KindInstance = "instance"

	CommentPublic   = "flynn-public"
	CommentCluster  = "flynn-cluster"
	CommentPeer     = "flynn-peer"
	CommentExpose   = "flynn-expose"
	CommentInstance = "flynn-instance"
)

// Rule is one UFW-style ingress allow. flynn-host only adds/removes KindPeer
// and KindExpose; public ports and RFC1918 CIDRs stay with the installer.
type Rule struct {
	Kind    string
	Port    int    // TCP port; 0 means any (from-IP / CIDR allows)
	From    string // IP or CIDR; empty means Anywhere
	Comment string
}

func (r Rule) Key() string {
	return r.Kind + "|" + strconv.Itoa(r.Port) + "|" + r.From
}

func (r Rule) String() string {
	src := r.From
	if src == "" {
		src = "Anywhere"
	}
	if r.Port > 0 {
		return fmt.Sprintf("allow %d/tcp from %s # %s", r.Port, src, r.Comment)
	}
	return fmt.Sprintf("allow from %s # %s", src, r.Comment)
}

// Desired is the flynn-host-managed slice of the firewall: known peer IPs and
// TCP ports published by routes. Public 22/80/443 and cluster CIDRs are not
// included; install-time hardening owns those.
type Desired struct {
	PeerIPs    []string
	ExposedTCP []int
	SelfIP     string
}

func PublicTCPPorts() []int {
	return []int{22, 80, 443}
}

func ClusterCIDRs() []string {
	return []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
}

func isPublicPort(port int) bool {
	for _, p := range PublicTCPPorts() {
		if p == port {
			return true
		}
	}
	return false
}

// ParsePeerIP accepts a bare IP or host:port and returns the host IP.
func ParsePeerIP(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("peer IP is required")
	}
	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() == nil {
			return "", fmt.Errorf("peer IP must be IPv4: %s", s)
		}
		return ip.String(), nil
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("invalid peer IP %q", s)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("invalid peer IP %q", s)
	}
	return ip.String(), nil
}

func NormalizePeers(addrs []string, selfIP string) []string {
	selfIP = strings.TrimSpace(selfIP)
	seen := map[string]struct{}{}
	var out []string
	for _, a := range addrs {
		ip, err := ParsePeerIP(a)
		if err != nil {
			continue
		}
		if selfIP != "" && ip == selfIP {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func NormalizePorts(ports []int) []int {
	seen := map[int]struct{}{}
	var out []int
	for _, p := range ports {
		if p <= 0 || p > 65535 || isPublicPort(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// TCPPortsFromRoutes returns listen ports for TCP routes (not HTTP 80/443).
func TCPPortsFromRoutes(routes []*router.Route) []int {
	var ports []int
	for _, r := range routes {
		if r == nil || r.Type != "tcp" {
			continue
		}
		ports = append(ports, int(r.Port))
	}
	return NormalizePorts(ports)
}

func Plan(d Desired) []Rule {
	var rules []Rule
	for _, ip := range NormalizePeers(d.PeerIPs, d.SelfIP) {
		rules = append(rules, Rule{
			Kind:    KindPeer,
			From:    ip,
			Comment: CommentPeer,
		})
	}
	for _, port := range NormalizePorts(d.ExposedTCP) {
		rules = append(rules, Rule{
			Kind:    KindExpose,
			Port:    port,
			Comment: CommentExpose,
		})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Key() < rules[j].Key() })
	return rules
}

func Managed(rules []Rule) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.Kind == KindPeer || r.Kind == KindExpose {
			out = append(out, r)
		}
	}
	return out
}

func Diff(have, want []Rule) (add, remove []Rule) {
	return diffRules(Managed(have), Managed(want))
}

// InstancePorts is the TCP ports this host should allow for database instances
// whose jobs are running here. Other instances' ports are omitted.
func InstancePorts(hostID string, jobs []instanceport.Job) []int {
	return instanceport.PortsForHost(hostID, jobs)
}

// InstanceRules are flynn-instance allows for ports. Route exposes stay KindExpose.
func InstanceRules(ports []int) []Rule {
	var rules []Rule
	for _, port := range NormalizePorts(ports) {
		rules = append(rules, Rule{
			Kind:    KindInstance,
			Port:    port,
			Comment: CommentInstance,
		})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Key() < rules[j].Key() })
	return rules
}

// InstanceManaged returns per-instance allows. Route reconcile ignores these
// so a TCP route sync does not close a database instance port.
func InstanceManaged(rules []Rule) []Rule {
	var out []Rule
	for _, r := range rules {
		if r.Kind == KindInstance {
			out = append(out, r)
		}
	}
	return out
}

func diffRules(have, want []Rule) (add, remove []Rule) {
	hm := map[string]Rule{}
	wm := map[string]Rule{}
	for _, r := range have {
		hm[r.Key()] = r
	}
	for _, r := range want {
		wm[r.Key()] = r
	}
	for k, r := range wm {
		if _, ok := hm[k]; !ok {
			add = append(add, r)
		}
	}
	for k, r := range hm {
		if _, ok := wm[k]; !ok {
			remove = append(remove, r)
		}
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Key() < add[j].Key() })
	sort.Slice(remove, func(i, j int) bool { return remove[i].Key() < remove[j].Key() })
	return add, remove
}

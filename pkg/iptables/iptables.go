package iptables

// This package is originally from Docker and has been modified for use by the
// Flynn project. See the NOTICE and LICENSE files for licensing and copyright
// details.

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/randy-girard/flynn/pkg/netpolicy"
)

var (
	ErrIptablesNotFound = errors.New("Iptables not found")
	supportsXlock       = false
)

type ChainError struct {
	Chain  string
	Output []byte
}

func (e *ChainError) Error() string {
	return fmt.Sprintf("iptables: %s: %s", e.Chain, string(e.Output))
}

func init() {
	supportsXlock = exec.Command("iptables", "--wait", "-L", "-n").Run() == nil
}

// EnableOutboundNAT sets up MASQUERADE for container traffic leaving the
// bridge toward the underlay, while leaving overlay→overlay traffic alone.
//
// Without excluding the overlay destination range, cross-node VXLAN traffic is
// SNAT'd to the local flannel VTEP (.0) address and peers never see the real
// container source IP — the same failure mode hostgw avoids with
// `! -d <overlay>`.
// OutboundMasqueradeArgs is the POSTROUTING MASQUERADE rule for underlay
// egress. Overlay destinations are excluded (! -d overlay) so cross-node
// VXLAN traffic keeps the real container source IP. The excluded network is
// the /16 inferred from a host subnet. A wider live overlay uses
// OutboundMasqueradeArgsFor.
func OutboundMasqueradeArgs(bridge, network string) []string {
	return []string{"POSTROUTING", "-t", "nat", "-s", network, "!", "-o", bridge, "!", "-d", OverlayNetworkFor(network), "-j", "MASQUERADE"}
}

// OutboundMasqueradeArgsFor is the masquerade rule that excludes explicit
// overlay instead of the /16 inferred from the host subnet. hostSubnet is
// masked to its network address so the rule matches the kernel's saved form.
func OutboundMasqueradeArgsFor(bridge, hostSubnet, overlay string) []string {
	return []string{"POSTROUTING", "-t", "nat", "-s", canonicalCIDR(hostSubnet), "!", "-o", bridge, "!", "-d", ExclusionCIDR(hostSubnet, overlay), "-j", "MASQUERADE"}
}

// ExclusionCIDR is the destination omitted from MASQUERADE. An empty or
// unparseable explicit overlay keeps OverlayNetworkFor, so a default /16
// install is unchanged.
func ExclusionCIDR(hostSubnet, explicit string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		return OverlayNetworkFor(hostSubnet)
	}
	ip, network, err := net.ParseCIDR(explicit)
	if err != nil || ip.To4() == nil || network == nil {
		return OverlayNetworkFor(hostSubnet)
	}
	return network.String()
}

// NeedsExplicitExclusion reports whether explicit names a different overlay
// than the /16 inferred from hostSubnet. Empty explicit does not.
func NeedsExplicitExclusion(hostSubnet, explicit string) bool {
	if strings.TrimSpace(explicit) == "" {
		return false
	}
	return ExclusionCIDR(hostSubnet, explicit) != OverlayNetworkFor(hostSubnet)
}

func canonicalCIDR(cidr string) string {
	_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || network == nil {
		return strings.TrimSpace(cidr)
	}
	return network.String()
}

// LegacyOutboundMasqueradeArgs is the pre-fix rule that SNAT'd overlay
// destinations to the local VTEP and blackholed VXLAN.
func LegacyOutboundMasqueradeArgs(bridge, network string) []string {
	return []string{"POSTROUTING", "-t", "nat", "-s", network, "!", "-o", bridge, "-j", "MASQUERADE"}
}

// OverlayIncomingForwardArgs accepts NEW overlay connections to local
// containers. ESTABLISHED-only is not enough for peers initiating to this host.
func OverlayIncomingForwardArgs(network, bridge string) []string {
	return []string{"FORWARD", "-d", network, "-o", bridge, "-j", "ACCEPT"}
}

// EnableOutboundNAT installs overlay-safe NAT and FORWARD rules.
func EnableOutboundNAT(bridge, network string) error {
	natArgs := OutboundMasqueradeArgs(bridge, network)
	if !Exists(natArgs...) {
		// Drop the legacy rule that MASQUERADE'd all non-bridge egress,
		// including overlay destinations (breaks flannel VXLAN).
		legacy := LegacyOutboundMasqueradeArgs(bridge, network)
		if Exists(legacy...) {
			_, _ = Raw(append([]string{"-D"}, legacy...)...)
		}
		if output, err := Raw(append([]string{"-I"}, natArgs...)...); err != nil {
			return fmt.Errorf("Unable to enable network bridge NAT: %s", err)
		} else if len(output) != 0 {
			return &ChainError{Chain: "POSTROUTING", Output: output}
		}
	}

	// Accept all non-intercontainer outgoing packets
	outgoingArgs := []string{"FORWARD", "-i", bridge, "!", "-o", bridge, "-j", "ACCEPT"}
	if !Exists(outgoingArgs...) {
		if output, err := Raw(append([]string{"-I"}, outgoingArgs...)...); err != nil {
			return fmt.Errorf("Unable to allow outgoing packets: %s", err)
		} else if len(output) != 0 {
			return &ChainError{Chain: "FORWARD outgoing", Output: output}
		}
	}

	incomingArgs := OverlayIncomingForwardArgs(network, bridge)
	if !Exists(incomingArgs...) {
		if output, err := Raw(append([]string{"-I"}, incomingArgs...)...); err != nil {
			return fmt.Errorf("Unable to allow incoming packets: %s", err)
		} else if len(output) != 0 {
			return &ChainError{Chain: "FORWARD incoming", Output: output}
		}
	}

	// Keep ESTABLISHED for backwards compatibility with older rule sets / policy DROP.
	existingArgs := []string{"FORWARD", "-o", bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}
	if !Exists(existingArgs...) {
		if output, err := Raw(append([]string{"-I"}, existingArgs...)...); err != nil {
			return fmt.Errorf("Unable to allow established incoming packets: %s", err)
		} else if len(output) != 0 {
			return &ChainError{Chain: "FORWARD established", Output: output}
		}
	}

	return nil
}

// UserOverlayDropArgs drops NEW overlay packets from user jobs. ESTABLISHED
// replies (router → user HTTP/TCP) must still pass; without --ctstate NEW this
// rule blackholes backends whenever the web job is not on the same host as the
// router. Datastore and local-bridge exceptions must be inserted before this
// rule.
func UserOverlayDropArgs(overlay string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-d", overlay, "-m", "conntrack", "--ctstate", "NEW", "-j", "DROP"}
}

// LegacyUserOverlayDropArgs is the pre-fix DROP that matched every state,
// including ESTABLISHED replies to the router (5-node HTTP 503).
func LegacyUserOverlayDropArgs(overlay string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-d", overlay, "-j", "DROP"}
}

// LegacyUserToDatastoreArgs is the pre-fix ACCEPT that matched every
// protocol/port on flynn-net-data, including appliance admin HTTP
// (postgres :5433, mariadb :3307, mongodb :27018, redis :6380, kafka :9095,
// clickhouse :9090).
func LegacyUserToDatastoreArgs() []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-m", "set", "--match-set", netpolicy.ServiceData, "dst", "-j", "ACCEPT"}
}

// UserToDatastoreArgs allows user jobs to reach one TCP data-plane port on
// appliance IPs (the hosts in DATABASE_URL / REDIS_URL / …).
func UserToDatastoreArgs(dport string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-m", "set", "--match-set", netpolicy.ServiceData, "dst", "-p", "tcp", "--dport", dport, "-j", "ACCEPT"}
}

// UserToDatastoreRules is one ACCEPT per UserDatastoreTCPPorts entry.
func UserToDatastoreRules() [][]string {
	ports := netpolicy.UserDatastoreTCPPorts
	rules := make([][]string, 0, len(ports))
	for _, p := range ports {
		rules = append(rules, UserToDatastoreArgs(p))
	}
	return rules
}

// LegacyUserToBridgeArgs is the pre-fix rule that accepted every protocol/port
// on the overlay gateway, including host SSH and discoverd HTTP when those
// sockets are bound on the bridge address.
func LegacyUserToBridgeArgs(bridgeAddr string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-d", bridgeAddr, "-j", "ACCEPT"}
}

// UserToBridgeDNSArgs allows user jobs to reach discoverd DNS on the overlay
// gateway. proto is "udp" or "tcp".
func UserToBridgeDNSArgs(bridgeAddr, proto string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-d", bridgeAddr, "-p", proto, "--dport", "53", "-j", "ACCEPT"}
}

// NodeSet is the ipset of host underlay IPs (this node and cluster peers).
// It is not a discoverd service; flynn-host fills it from peer membership.
const NodeSet = "flynn-net-nodes"

// UserToNodeDropArgs drops NEW user-job packets to cluster node underlay IPs
// (SSH, public discoverd, host HTTP APIs) after MASQUERADE toward the underlay.
func UserToNodeDropArgs() []string {
	return toNodeDropArgs(netpolicy.ServiceUser)
}

func BuildToNodeDropArgs() []string {
	return toNodeDropArgs(netpolicy.ServiceBuild)
}

func toNodeDropArgs(set string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", set, "src", "-m", "set", "--match-set", NodeSet, "dst", "-m", "conntrack", "--ctstate", "NEW", "-j", "DROP"}
}

// UserToHostDNSArgs allows user overlay IPs to query host DNS (INPUT; the
// gateway address is a local IP, so these packets do not traverse FORWARD).
func UserToHostDNSArgs(proto string) []string {
	return toHostDNSArgs(netpolicy.ServiceUser, proto)
}

func BuildToHostDNSArgs(proto string) []string {
	return toHostDNSArgs(netpolicy.ServiceBuild, proto)
}

func toHostDNSArgs(set, proto string) []string {
	return []string{"INPUT", "-m", "set", "--match-set", set, "src", "-p", proto, "--dport", "53", "-j", "ACCEPT"}
}

// UserToHostDiscoverdArgs is the pre-fix INPUT allow for discoverd HTTP on
// the overlay gateway. flynn-host now registers user services itself, so
// this hole is removed on upgrade.
func UserToHostDiscoverdArgs(bridgeAddr string) []string {
	return toHostDiscoverdArgs(netpolicy.ServiceUser, bridgeAddr)
}

func BuildToHostDiscoverdArgs(bridgeAddr string) []string {
	return toHostDiscoverdArgs(netpolicy.ServiceBuild, bridgeAddr)
}

func toHostDiscoverdArgs(set, bridgeAddr string) []string {
	return []string{"INPUT", "-m", "set", "--match-set", set, "src", "-d", bridgeAddr, "-p", "tcp", "--dport", netpolicy.DiscoverdHTTPPort, "-j", "ACCEPT"}
}

// UserToHostDropArgs drops NEW packets from user jobs to any host-local
// address (SSH :22, :80/:443, host API, public-IP discoverd).
func UserToHostDropArgs() []string {
	return toHostDropArgs(netpolicy.ServiceUser)
}

func BuildToHostDropArgs() []string {
	return toHostDropArgs(netpolicy.ServiceBuild)
}

func toHostDropArgs(set string) []string {
	return []string{"INPUT", "-m", "set", "--match-set", set, "src", "-m", "conntrack", "--ctstate", "NEW", "-j", "DROP"}
}

// DataToHostPortDropArgs drops one NEW TCP port from data-plane overlay IPs
// to a host-local address. port is "22" (SSH) or "1113" (host API). DNS and
// discoverd are not covered by this rule.
func DataToHostPortDropArgs(port string) []string {
	return []string{"INPUT", "-m", "set", "--match-set", netpolicy.ServiceData, "src", "-p", "tcp", "--dport", port, "-m", "conntrack", "--ctstate", "NEW", "-j", "DROP"}
}

// DataToNodePortDropArgs drops that same port when the packet is forwarded
// toward a cluster node underlay IP.
func DataToNodePortDropArgs(port string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceData, "src", "-m", "set", "--match-set", NodeSet, "dst", "-p", "tcp", "--dport", port, "-m", "conntrack", "--ctstate", "NEW", "-j", "DROP"}
}

// DataToHostDNSArgs allows data-plane jobs to query host DNS.
func DataToHostDNSArgs(proto string) []string {
	return toHostDNSArgs(netpolicy.ServiceData, proto)
}

// DataToHostDiscoverdArgs allows data-plane jobs to register and watch
// discoverd. Engines dial a host address; a blanket INPUT drop would break
// leader election.
func DataToHostDiscoverdArgs() []string {
	return []string{"INPUT", "-m", "set", "--match-set", netpolicy.ServiceData, "src", "-p", "tcp", "--dport", netpolicy.DiscoverdHTTPPort, "-j", "ACCEPT"}
}

// NodeIPs is the underlay address list for NodeSet: this host plus peers.
func NodeIPs(self string, peers []string) []net.IP {
	return UnionIPs(nil, parseIPv4s(append([]string{self}, peers...)))
}

func parseIPv4s(addrs []string) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, s := range addrs {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4)
		}
	}
	return out
}

// BuildToUserDropArgs stops slug/docker builders from dialing user jobs.
func BuildToUserDropArgs() []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceBuild, "src", "-m", "set", "--match-set", netpolicy.ServiceUser, "dst", "-j", "DROP"}
}

// IsolationSets are the ipsets mirrored from discoverd netpolicy services.
func IsolationSets() []string {
	return []string{netpolicy.ServiceUser, netpolicy.ServiceBuild, netpolicy.ServiceData, netpolicy.ServiceSys}
}

// EnableJobIsolation installs default-deny overlay rules for user jobs and
// host-local INPUT drops so tenants cannot open SSH, public discoverd, or
// node APIs. Call after EnableOutboundNAT. Requires ipset. Same-host L2
// isolation also needs br_netfilter (caller). egressDeny (from
// EgressDenyCIDRs) is dropped for user and build jobs ahead of the bridge
// egress ACCEPT.
func EnableJobIsolation(overlay, bridgeAddr string, egressDeny []string) error {
	if err := EnsureSets(append(IsolationSets(), NodeSet)...); err != nil {
		return err
	}
	legacyDrop := LegacyUserOverlayDropArgs(overlay)
	if Exists(legacyDrop...) {
		_, _ = Raw(append([]string{"-D"}, legacyDrop...)...)
	}
	legacyBridge := LegacyUserToBridgeArgs(bridgeAddr)
	if Exists(legacyBridge...) {
		_, _ = Raw(append([]string{"-D"}, legacyBridge...)...)
	}
	legacyData := LegacyUserToDatastoreArgs()
	if Exists(legacyData...) {
		_, _ = Raw(append([]string{"-D"}, legacyData...)...)
	}
	// Insert last-to-first so the chain order is: data ACCEPT (per port),
	// DNS ACCEPT, build→user DROP, node DROP, user overlay DROP.
	iso := [][]string{
		UserOverlayDropArgs(overlay),
		UserToNodeDropArgs(),
		BuildToNodeDropArgs(),
		DataToNodePortDropArgs("22"),
		DataToNodePortDropArgs("1113"),
		BuildToUserDropArgs(),
		UserToBridgeDNSArgs(bridgeAddr, "tcp"),
		UserToBridgeDNSArgs(bridgeAddr, "udp"),
	}
	iso = append(iso, UserToDatastoreRules()...)
	for _, args := range iso {
		if err := insertIfMissing("FORWARD isolation", args); err != nil {
			return err
		}
	}
	if err := ApplyEgressDeny(func(args ...string) ([]byte, error) {
		return Raw(args...)
	}, egressDeny); err != nil {
		return err
	}
	return EnableHostIsolation(bridgeAddr)
}

// EnableHostIsolation installs INPUT rules so user and build overlay IPs can
// only open DNS. Discoverd HTTP is no longer reachable from those jobs;
// flynn-host registers their backends.
func EnableHostIsolation(bridgeAddr string) error {
	for _, args := range [][]string{
		UserToHostDiscoverdArgs(bridgeAddr),
		BuildToHostDiscoverdArgs(bridgeAddr),
	} {
		if Exists(args...) {
			_, _ = Raw(append([]string{"-D"}, args...)...)
		}
	}
	// Last-to-first: DROP, TCP/53, UDP/53 — per untrusted set.
	for _, args := range [][]string{
		UserToHostDropArgs(),
		BuildToHostDropArgs(),
		DataToHostPortDropArgs("22"),
		DataToHostPortDropArgs("1113"),
		UserToHostDNSArgs("tcp"),
		BuildToHostDNSArgs("tcp"),
		DataToHostDNSArgs("tcp"),
		UserToHostDNSArgs("udp"),
		BuildToHostDNSArgs("udp"),
		DataToHostDNSArgs("udp"),
		DataToHostDiscoverdArgs(),
	} {
		if err := insertIfMissing("INPUT host isolation", args); err != nil {
			return err
		}
	}
	return nil
}

func insertIfMissing(chain string, args []string) error {
	if Exists(args...) {
		return nil
	}
	output, err := Raw(append([]string{"-I"}, args...)...)
	if err != nil {
		return fmt.Errorf("unable to install job isolation: %s", err)
	}
	if len(output) != 0 {
		return &ChainError{Chain: chain, Output: output}
	}
	return nil
}

// RulePlan is an iptables edit derived from `iptables -S` text. Delete
// entries are arguments for `iptables -D` (chain, `-t`, table, match).
// Insert is arguments for `iptables -I`. InsertBefore is the 1-based rule
// number in that chain; 0 means insert at the head.
type RulePlan struct {
	Delete       [][]string
	Insert       []string
	InsertBefore int
}

// PlanMasquerade removes POSTROUTING MASQUERADE rules for hostSubnet whose
// overlay exclusion is not keep, including the legacy rule with no `-d`.
// Other sources are left alone. Insert is empty when keep is already present.
func PlanMasquerade(save, bridge, hostSubnet, keep string) RulePlan {
	source := canonicalCIDR(hostSubnet)
	keepNet := ExclusionCIDR(hostSubnet, keep)
	var plan RulePlan
	have := false
	for _, rule := range saveRules(save, "POSTROUTING") {
		if !isMasqueradeFor(rule, bridge, source) {
			continue
		}
		dest, ok := flagValue(rule, "-d")
		if ok && canonicalCIDR(dest) == keepNet {
			have = true
			continue
		}
		plan.Delete = append(plan.Delete, append([]string{"POSTROUTING", "-t", "nat"}, rule...))
	}
	if !have {
		plan.Insert = OutboundMasqueradeArgsFor(bridge, hostSubnet, keep)
	}
	return plan
}

// PlanForwardDrop removes user-overlay DROP rules whose destination is not
// keep, and inserts the keep rule immediately before bridge egress ACCEPT.
// Datastore and other accepts are not deleted. InsertBefore is that ACCEPT's
// current 1-based line so the caller inserts before deleting.
func PlanForwardDrop(save, bridge, keep string) RulePlan {
	keepNet := canonicalCIDR(keep)
	var plan RulePlan
	have := false
	for i, rule := range saveRules(save, "FORWARD") {
		if isBridgeEgress(rule, bridge) && plan.InsertBefore == 0 {
			plan.InsertBefore = i + 1
		}
		dest, ok := userOverlayDropDest(rule)
		if !ok {
			continue
		}
		if canonicalCIDR(dest) == keepNet {
			have = true
			continue
		}
		plan.Delete = append(plan.Delete, append([]string{"FORWARD"}, rule...))
	}
	if !have {
		plan.Insert = UserOverlayDropArgs(keepNet)
	}
	return plan
}

// IPTables runs one iptables invocation. args do not include the binary name.
type IPTables func(args ...string) ([]byte, error)

// ApplyOverlayRules replaces masquerade and user-overlay DROP destinations
// with overlay. The wider DROP is inserted before the bridge egress ACCEPT,
// and before the stale DROP is deleted, so datastore exceptions stay ahead.
func ApplyOverlayRules(run IPTables, bridge, hostSubnet, overlay string) error {
	if run == nil {
		return errors.New("iptables runner is nil")
	}
	natSave, err := run("-t", "nat", "-S", "POSTROUTING")
	if err != nil {
		return fmt.Errorf("reading nat rules: %s", err)
	}
	nat := PlanMasquerade(string(natSave), bridge, hostSubnet, overlay)
	if len(nat.Insert) > 0 {
		if _, err := run(append([]string{"-I"}, nat.Insert...)...); err != nil {
			return fmt.Errorf("installing overlay masquerade: %s", err)
		}
	}
	for _, rule := range nat.Delete {
		if _, err := run(append([]string{"-D"}, rule...)...); err != nil {
			return fmt.Errorf("removing stale masquerade: %s", err)
		}
	}
	fwdSave, err := run("-S", "FORWARD")
	if err != nil {
		return fmt.Errorf("reading filter rules: %s", err)
	}
	fwd := PlanForwardDrop(string(fwdSave), bridge, overlay)
	if len(fwd.Insert) > 0 {
		args := []string{"-I", "FORWARD"}
		if fwd.InsertBefore > 0 {
			args = append(args, strconv.Itoa(fwd.InsertBefore))
		}
		if len(fwd.Insert) > 1 {
			args = append(args, fwd.Insert[1:]...)
		}
		if _, err := run(args...); err != nil {
			return fmt.Errorf("installing overlay drop: %s", err)
		}
	}
	for _, rule := range fwd.Delete {
		if _, err := run(append([]string{"-D"}, rule...)...); err != nil {
			return fmt.Errorf("removing stale overlay drop: %s", err)
		}
	}
	return nil
}

// UseOverlayExclusion installs overlay as the live masquerade and DROP
// destination on this host. It is a no-op for the rule set when overlay
// already matches and no stale exclusion remains.
func UseOverlayExclusion(bridge, hostSubnet, overlay string) error {
	return ApplyOverlayRules(func(args ...string) ([]byte, error) {
		return Raw(args...)
	}, bridge, hostSubnet, overlay)
}

func saveRules(save, chain string) [][]string {
	var rules [][]string
	prefix := "-A " + chain + " "
	for _, line := range strings.Split(save, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		rules = append(rules, fields[2:])
	}
	return rules
}

func isMasqueradeFor(rule []string, bridge, source string) bool {
	if flagValueExact(rule, "-j") != "MASQUERADE" {
		return false
	}
	if canonicalCIDR(flagValueExact(rule, "-s")) != source {
		return false
	}
	return negatedFlag(rule, "-o") && flagValueExact(rule, "-o") == bridge
}

func isBridgeEgress(rule []string, bridge string) bool {
	return flagValueExact(rule, "-i") == bridge &&
		negatedFlag(rule, "-o") &&
		flagValueExact(rule, "-o") == bridge &&
		flagValueExact(rule, "-j") == "ACCEPT"
}

func userOverlayDropDest(rule []string) (string, bool) {
	if flagValueExact(rule, "-j") != "DROP" {
		return "", false
	}
	if _, ok := egressDenyKey(rule); ok {
		return "", false
	}
	joined := strings.Join(rule, " ")
	if !strings.Contains(joined, netpolicy.ServiceUser) || !strings.Contains(joined, "src") {
		return "", false
	}
	dest := flagValueExact(rule, "-d")
	if dest == "" {
		return "", false
	}
	return dest, true
}

func flagValueExact(rule []string, flag string) string {
	v, _ := flagValue(rule, flag)
	return v
}

func flagValue(rule []string, flag string) (string, bool) {
	for i := 0; i < len(rule); i++ {
		if rule[i] == flag && i+1 < len(rule) && !strings.HasPrefix(rule[i+1], "-") {
			return rule[i+1], true
		}
	}
	return "", false
}

func negatedFlag(rule []string, flag string) bool {
	for i := 0; i < len(rule); i++ {
		if rule[i] == flag && i > 0 && rule[i-1] == "!" {
			return true
		}
	}
	return false
}

// OverlayNetworkFor returns the flannel overlay CIDR that contains network.
// Flynn defaults to a /16 overlay with /24 host subnets (e.g. 100.64.57.1/24 →
// 100.64.0.0/16). When the local subnet is already as wide as /16 or wider,
// network itself is returned.
func OverlayNetworkFor(network string) string {
	ip, ipnet, err := net.ParseCIDR(network)
	if err != nil {
		return network
	}
	v4 := ip.To4()
	if v4 == nil {
		return ipnet.String()
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones <= 16 {
		return ipnet.String()
	}
	return fmt.Sprintf("%d.%d.0.0/16", v4[0], v4[1])
}

// Check if an existing rule exists
func Exists(args ...string) bool {
	if _, err := Raw(append([]string{"-C"}, args...)...); err != nil {
		return false
	}
	return true
}

func Raw(args ...string) ([]byte, error) {
	path, err := exec.LookPath("iptables")
	if err != nil {
		return nil, ErrIptablesNotFound
	}

	if supportsXlock {
		args = append([]string{"--wait"}, args...)
	}

	output, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("iptables failed: iptables %v: %s (%s)", strings.Join(args, " "), output, err)
	}

	// ignore iptables' message about xtables lock
	if strings.Contains(string(output), "waiting for it to exit") {
		output = []byte("")
	}

	return output, err
}

// EgressDenyComment tags the egress deny rules so ApplyEgressDeny can find
// and remove ones the operator dropped from the list.
const EgressDenyComment = "flynn-egress-deny"

// DefaultEgressDeny is always denied to user and build jobs: link-local,
// which holds the cloud instance metadata service (169.254.169.254 on AWS,
// GCP, Azure, and most others) with node credentials and user-data.
var DefaultEgressDeny = []string{"169.254.0.0/16"}

// EgressDenySets are the job classes whose egress the deny list covers.
func EgressDenySets() []string {
	return []string{netpolicy.ServiceUser, netpolicy.ServiceBuild}
}

// EgressDenyArgs drops packets from set's job IPs to cidr.
func EgressDenyArgs(set, cidr string) []string {
	return []string{"FORWARD", "-d", cidr, "-m", "set", "--match-set", set, "src", "-m", "comment", "--comment", EgressDenyComment, "-j", "DROP"}
}

// EgressDenyCIDRs is DefaultEgressDeny plus the operator's comma-separated
// list (FLYNN_JOB_EGRESS_DENY), deduplicated. A bare IP is a /32. An invalid
// entry is an error so a typo cannot silently leave a range open.
func EgressDenyCIDRs(extra string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range append(append([]string{}, DefaultEgressDeny...), strings.Split(extra, ",")...) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			entry += "/32"
		}
		_, ipnet, err := net.ParseCIDR(entry)
		if err != nil || ipnet.IP.To4() == nil {
			return nil, fmt.Errorf("invalid egress deny CIDR %q (want IPv4 a.b.c.d/n)", strings.TrimSpace(raw))
		}
		c := ipnet.String()
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

func egressDenyKey(rule []string) (string, bool) {
	if flagValueExact(rule, "--comment") != EgressDenyComment || flagValueExact(rule, "-j") != "DROP" {
		return "", false
	}
	set := flagValueExact(rule, "--match-set")
	dest := flagValueExact(rule, "-d")
	if set == "" || dest == "" {
		return "", false
	}
	return set + " " + canonicalCIDR(dest), true
}

// ApplyEgressDeny makes FORWARD hold exactly one DROP per (user/build set,
// cidr), at the head of the chain so it is ahead of the bridge egress
// ACCEPT. Rules with EgressDenyComment for CIDRs no longer listed are
// deleted after the new ones are in place.
func ApplyEgressDeny(run IPTables, cidrs []string) error {
	if run == nil {
		return errors.New("iptables runner is nil")
	}
	save, err := run("-S", "FORWARD")
	if err != nil {
		return fmt.Errorf("reading filter rules: %s", err)
	}
	have := map[string]bool{}
	var stale [][]string
	want := map[string]bool{}
	for _, set := range EgressDenySets() {
		for _, c := range cidrs {
			want[set+" "+canonicalCIDR(c)] = true
		}
	}
	for _, rule := range saveRules(string(save), "FORWARD") {
		key, ok := egressDenyKey(rule)
		if !ok {
			continue
		}
		if want[key] && !have[key] {
			have[key] = true
			continue
		}
		stale = append(stale, rule)
	}
	for _, set := range EgressDenySets() {
		for _, c := range cidrs {
			if have[set+" "+canonicalCIDR(c)] {
				continue
			}
			if _, err := run(append([]string{"-I"}, EgressDenyArgs(set, c)...)...); err != nil {
				return fmt.Errorf("installing egress deny %s for %s: %s", c, set, err)
			}
		}
	}
	for _, rule := range stale {
		if _, err := run(append([]string{"-D", "FORWARD"}, rule...)...); err != nil {
			return fmt.Errorf("removing stale egress deny: %s", err)
		}
	}
	return nil
}

package iptables

// This package is originally from Docker and has been modified for use by the
// Flynn project. See the NOTICE and LICENSE files for licensing and copyright
// details.

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
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
// VXLAN traffic keeps the real container source IP.
func OutboundMasqueradeArgs(bridge, network string) []string {
	return []string{"POSTROUTING", "-t", "nat", "-s", network, "!", "-o", bridge, "!", "-d", OverlayNetworkFor(network), "-j", "MASQUERADE"}
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

// UserToDatastoreArgs allows user jobs to reach appliance data-plane IPs
// (the hosts embedded in provisioned DATABASE_URL / REDIS_URL / …).
func UserToDatastoreArgs() []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-m", "set", "--match-set", netpolicy.ServiceData, "dst", "-j", "ACCEPT"}
}

// UserToBridgeArgs allows user jobs to reach the local gateway (discoverd DNS).
func UserToBridgeArgs(bridgeAddr string) []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceUser, "src", "-d", bridgeAddr, "-j", "ACCEPT"}
}

// BuildToUserDropArgs stops slug/docker builders from dialing user jobs.
func BuildToUserDropArgs() []string {
	return []string{"FORWARD", "-m", "set", "--match-set", netpolicy.ServiceBuild, "src", "-m", "set", "--match-set", netpolicy.ServiceUser, "dst", "-j", "DROP"}
}

// IsolationSets are the ipsets mirrored from discoverd netpolicy services.
func IsolationSets() []string {
	return []string{netpolicy.ServiceUser, netpolicy.ServiceBuild, netpolicy.ServiceData, netpolicy.ServiceSys}
}

// EnableJobIsolation installs default-deny overlay rules for user jobs.
// Call after EnableOutboundNAT. Requires the ipset kernel module and the
// ipset binary. Same-host L2 isolation also needs br_netfilter (caller).
func EnableJobIsolation(overlay, bridgeAddr string) error {
	if err := EnsureSets(IsolationSets()...); err != nil {
		return err
	}
	legacyDrop := LegacyUserOverlayDropArgs(overlay)
	if Exists(legacyDrop...) {
		_, _ = Raw(append([]string{"-D"}, legacyDrop...)...)
	}
	// Insert last-to-first so the chain order is: data ACCEPT, bridge ACCEPT,
	// build→user DROP, user overlay DROP (before the broad incoming ACCEPT).
	for _, args := range [][]string{
		UserOverlayDropArgs(overlay),
		BuildToUserDropArgs(),
		UserToBridgeArgs(bridgeAddr),
		UserToDatastoreArgs(),
	} {
		if Exists(args...) {
			continue
		}
		if output, err := Raw(append([]string{"-I"}, args...)...); err != nil {
			return fmt.Errorf("unable to install job isolation: %s", err)
		} else if len(output) != 0 {
			return &ChainError{Chain: "FORWARD isolation", Output: output}
		}
	}
	return nil
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

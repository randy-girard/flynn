package hostfw

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const DefaultStatePath = "/var/lib/flynn/host-firewall.json"

// Extra is operator-pinned peers/ports that survive until discoverd/controller
// catch up (for example flynn-host init --peer-ips before the daemon joins).
type Extra struct {
	Peers []string `json:"peers,omitempty"`
	Ports []int    `json:"ports,omitempty"`
}

func StatePath() string {
	if p := os.Getenv("FLYNN_HOST_FIREWALL_STATE"); p != "" {
		return p
	}
	return DefaultStatePath
}

func LoadExtra(path string) Extra {
	data, err := os.ReadFile(path)
	if err != nil {
		return Extra{}
	}
	var e Extra
	if json.Unmarshal(data, &e) != nil {
		return Extra{}
	}
	e.Peers = NormalizePeers(e.Peers, "")
	e.Ports = NormalizePorts(e.Ports)
	return e
}

func SaveExtra(path string, e Extra) error {
	e.Peers = NormalizePeers(e.Peers, "")
	e.Ports = NormalizePorts(e.Ports)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func (e Extra) WithPeer(ip string) Extra {
	e.Peers = NormalizePeers(append(append([]string{}, e.Peers...), ip), "")
	return e
}

func (e Extra) WithoutPeer(ip string) Extra {
	ip, err := ParsePeerIP(ip)
	if err != nil {
		return e
	}
	var rest []string
	for _, p := range e.Peers {
		if p != ip {
			rest = append(rest, p)
		}
	}
	e.Peers = rest
	return e
}

func (e Extra) WithPort(port int) Extra {
	e.Ports = NormalizePorts(append(append([]int{}, e.Ports...), port))
	return e
}

func (e Extra) WithoutPort(port int) Extra {
	var rest []int
	for _, p := range e.Ports {
		if p != port {
			rest = append(rest, p)
		}
	}
	e.Ports = rest
	return e
}

func MergeDesired(selfIP string, extra Extra, livePeers []string, livePorts []int) Desired {
	peers := append(append([]string{}, extra.Peers...), livePeers...)
	ports := append(append([]int{}, extra.Ports...), livePorts...)
	return Desired{
		SelfIP:     selfIP,
		PeerIPs:    peers,
		ExposedTCP: ports,
	}
}

// ApplySeedPeers allows the given node IPs now. It does not persist them;
// flynn-host daemon keeps --peer-ips in memory until discoverd lists hosts.
func ApplySeedPeers(peers []string) error {
	return Reconcile(UFWBackend{}, Desired{PeerIPs: peers})
}

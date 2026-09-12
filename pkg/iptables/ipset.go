package iptables

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func ipsetBin() (string, error) {
	path, err := exec.LookPath("ipset")
	if err != nil {
		return "", fmt.Errorf("ipset not found (install the ipset package): %w", err)
	}
	return path, nil
}

func ipset(args ...string) error {
	bin, err := ipsetBin()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ipset %s: %s (%s)", strings.Join(args, " "), bytesPreview(out), err)
	}
	return nil
}

// UnionIPs returns unique IPs from a then b. Used so a discoverd snapshot
// cannot drop overlay IPs this host already tracks.
func UnionIPs(a, b []net.IP) []net.IP {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]net.IP, 0, len(a)+len(b))
	for _, group := range [][]net.IP{a, b} {
		for _, ip := range group {
			if ip == nil {
				continue
			}
			k := ip.String()
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, ip)
		}
	}
	return out
}

func bytesPreview(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// EnsureSets creates hash:ip sets if missing.
func EnsureSets(names ...string) error {
	for _, name := range names {
		if err := ipset("create", name, "hash:ip", "family", "inet", "-exist"); err != nil {
			return err
		}
	}
	return nil
}

// AddSetIP adds addr to set. Missing sets are created.
func AddSetIP(set string, addr net.IP) error {
	if addr == nil {
		return nil
	}
	if err := EnsureSets(set); err != nil {
		return err
	}
	return ipset("add", set, addr.String(), "-exist")
}

// DelSetIP removes addr from set. Missing entries are ignored.
func DelSetIP(set string, addr net.IP) error {
	if addr == nil {
		return nil
	}
	err := ipset("del", set, addr.String())
	if err != nil && strings.Contains(err.Error(), "not added") {
		return nil
	}
	return err
}

// ReplaceSetIPs flushes set and adds addrs.
func ReplaceSetIPs(set string, addrs []net.IP) error {
	if err := EnsureSets(set); err != nil {
		return err
	}
	if err := ipset("flush", set); err != nil {
		return err
	}
	for _, addr := range addrs {
		if addr == nil {
			continue
		}
		if err := ipset("add", set, addr.String(), "-exist"); err != nil {
			return err
		}
	}
	return nil
}

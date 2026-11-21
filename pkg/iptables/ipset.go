package iptables

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
)

func ipsetBin() (string, error) {
	path, err := exec.LookPath("ipset")
	if err != nil {
		return "", fmt.Errorf("ipset not found (install the ipset package): %w", err)
	}
	return path, nil
}

// ipsetExec runs the ipset binary with args, feeding stdin when it is not
// nil. Tests replace it with a recorder.
var ipsetExec = func(stdin []byte, args ...string) ([]byte, error) {
	bin, err := ipsetBin()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func ipset(args ...string) error {
	out, err := ipsetExec(nil, args...)
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
	unlock := lockSet(set)
	defer unlock()
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
	unlock := lockSet(set)
	defer unlock()
	err := ipset("del", set, addr.String())
	if err != nil && strings.Contains(err.Error(), "not added") {
		return nil
	}
	return err
}

// setLocks serialises changes to one named set, so an incremental add or
// delete cannot land on the temporary set between fill and swap, and two
// replaces of the same set cannot share the temporary name.
var setLocks sync.Map // set name -> *sync.Mutex

func lockSet(set string) func() {
	m, _ := setLocks.LoadOrStore(set, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// tempSetName is the scratch set ReplaceSetIPs fills before the swap. ipset
// names are at most 31 characters.
func tempSetName(set string) string { return set + "-swap" }

// ReplaceSetIPs makes set contain exactly addrs without ever emptying it.
// Every isolation rule matches `--match-set <set>`, so a flush followed by
// one add per IP left user jobs unmatched (unrestricted) until the last add.
// One `ipset restore` fills a temporary set and swaps it with the live set;
// the swap is a single kernel operation, so a packet sees either the old or
// the new contents. On error the live set is left as it was.
func ReplaceSetIPs(set string, addrs []net.IP) error {
	unlock := lockSet(set)
	defer unlock()
	if err := EnsureSets(set); err != nil {
		return err
	}
	tmp := tempSetName(set)
	var b strings.Builder
	fmt.Fprintf(&b, "create %s hash:ip family inet\n", tmp)
	fmt.Fprintf(&b, "flush %s\n", tmp)
	for _, addr := range addrs {
		if addr == nil {
			continue
		}
		fmt.Fprintf(&b, "add %s %s\n", tmp, addr.String())
	}
	fmt.Fprintf(&b, "swap %s %s\n", tmp, set)
	fmt.Fprintf(&b, "destroy %s\n", tmp)
	out, err := ipsetExec([]byte(b.String()), "-exist", "restore")
	if err != nil {
		return fmt.Errorf("ipset restore %s: %s (%s)", set, bytesPreview(out), err)
	}
	return nil
}

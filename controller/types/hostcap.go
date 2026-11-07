package types

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// DefaultHostMaxProcesses is how many tenant jobs fit on a host with memTotal
// bytes when the jobs use the smallest positive memory in memories.
// overlayAddrs is the container address count for the host subnet. Zero means
// no overlay ceiling. The result is 0 when memory or the catalog cannot
// produce a cap. A positive memory and a runtime larger than the host yield 1.
// Callers must not persist 0: EncodeHostMaxProcesses(0) clears the cap.
func DefaultHostMaxProcesses(memTotal int64, memories []int64, overlayAddrs int) int {
	if memTotal <= 0 {
		return 0
	}
	var slot int64
	for _, m := range memories {
		if m <= 0 {
			continue
		}
		if slot == 0 || m < slot {
			slot = m
		}
	}
	if slot <= 0 {
		return 0
	}
	count := int(memTotal / slot)
	if count < 1 {
		count = 1
	}
	if overlayAddrs > 0 && count > overlayAddrs {
		count = overlayAddrs
	}
	return count
}

// OverlayContainerAddrs is how many container addresses a subnet of the given
// prefix has. The allocator excludes the network and broadcast addresses, and
// the bridge takes the first usable address, so the count is 2^(32-prefix)-3.
// A /24 is 253.
func OverlayContainerAddrs(prefix uint) int {
	if prefix == 0 || prefix >= 32 {
		return 0
	}
	size := 1 << (32 - prefix)
	n := size - 3
	if n < 1 {
		return 0
	}
	return n
}

// DefaultOverlayContainerAddrs is the ceiling used before this host has a
// subnet. Empty flannel NETWORK is 100.100.0.0/16, and SubnetLen becomes 24.
func DefaultOverlayContainerAddrs() int {
	return OverlayContainerAddrs(24)
}

// OverlayContainerAddrsFromCIDR reads the prefix of cidr. The address does
// not have to be the network address.
func OverlayContainerAddrsFromCIDR(cidr string) (int, bool) {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return 0, false
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, false
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones <= 0 {
		return 0, false
	}
	n := OverlayContainerAddrs(uint(ones))
	if n < 1 {
		return 0, false
	}
	return n, true
}

// ParseMemTotal reads MemTotal from a /proc/meminfo body. The value is
// kibibytes in the file and bytes in the result.
func ParseMemTotal(r io.Reader) (int64, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb <= 0 {
			return 0, fmt.Errorf("invalid MemTotal %q", fields[1])
		}
		return kb * 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal missing")
}

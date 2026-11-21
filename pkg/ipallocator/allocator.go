// Copyright 2015 Docker, Inc. Code released under the Apache 2.0 license.

package ipallocator

import (
	"errors"
	"math/big"
	"net"
	"sync"
	"time"
)

// allocatedMap is thread-unsafe set of allocated IP
type allocatedMap struct {
	p map[string]struct{}
	// held are released addresses in quarantine, with the time they may be
	// handed out again. Each one is also in p, so getNextIP skips it.
	held  map[string]time.Time
	last  *big.Int
	begin *big.Int
	end   *big.Int
}

func newAllocatedMap(network *net.IPNet) *allocatedMap {
	firstIP, lastIP := networkRange(network)
	begin := big.NewInt(0).Add(ipToBigInt(firstIP), big.NewInt(1))
	end := big.NewInt(0).Sub(ipToBigInt(lastIP), big.NewInt(1))

	return &allocatedMap{
		p:     make(map[string]struct{}),
		held:  make(map[string]time.Time),
		begin: begin,
		end:   end,
		last:  big.NewInt(0).Sub(begin, big.NewInt(1)), // so first allocated will be begin
	}
}

type networkSet map[string]*allocatedMap

var (
	// ErrNoAvailableIPs preformatted error
	ErrNoAvailableIPs = errors.New("no available ip addresses on network")
	// ErrIPAlreadyAllocated preformatted error
	ErrIPAlreadyAllocated = errors.New("ip already allocated")
	// ErrIPOutOfRange preformatted error
	ErrIPOutOfRange = errors.New("requested ip is out of range")
	// ErrNetworkAlreadyRegistered preformatted error
	ErrNetworkAlreadyRegistered = errors.New("network already registered")
	// ErrBadSubnet preformatted error
	ErrBadSubnet = errors.New("network does not contain specified subnet")
)

// IPAllocator manages the ipam
type IPAllocator struct {
	allocatedIPs networkSet
	mutex        sync.Mutex
	now          func() time.Time
}

// New returns a new instance of IPAllocator
func New() *IPAllocator {
	return &IPAllocator{allocatedIPs: networkSet{}, now: time.Now}
}

// RegisterSubnet registers network in global allocator with bounds
// defined by subnet. If you want to use network range you must call
// this method before first RequestIP, otherwise full network range will be used
func (a *IPAllocator) RegisterSubnet(network *net.IPNet, subnet *net.IPNet) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	nw := &net.IPNet{IP: network.IP.Mask(network.Mask), Mask: network.Mask}
	key := nw.String()
	if _, ok := a.allocatedIPs[key]; ok {
		return ErrNetworkAlreadyRegistered
	}

	// Check that subnet is within network
	beginIP, endIP := networkRange(subnet)
	if !(network.Contains(beginIP) && network.Contains(endIP)) {
		return ErrBadSubnet
	}

	n := newAllocatedMap(subnet)
	a.allocatedIPs[key] = n
	return nil
}

// RequestIP requests an available ip from the given network.  It
// will return the next available ip if the ip provided is nil.  If the
// ip provided is not nil it will validate that the provided ip is available
// for use or return an error
func (a *IPAllocator) RequestIP(network *net.IPNet, ip net.IP) (net.IP, error) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	nw := &net.IPNet{IP: network.IP.Mask(network.Mask), Mask: network.Mask}
	key := nw.String()
	allocated, ok := a.allocatedIPs[key]
	if !ok {
		allocated = newAllocatedMap(nw)
		a.allocatedIPs[key] = allocated
	}

	allocated.expireHeld(a.now())
	if ip == nil {
		return allocated.getNextIP()
	}
	return allocated.checkIP(ip)
}

// ReleaseIP adds the provided ip back into the pool of
// available ips to be returned for use.
func (a *IPAllocator) ReleaseIP(network *net.IPNet, ip net.IP) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	nw := &net.IPNet{IP: network.IP.Mask(network.Mask), Mask: network.Mask}
	if allocated, exists := a.allocatedIPs[nw.String()]; exists {
		delete(allocated.p, ip.String())
		delete(allocated.held, ip.String())
	}
	return nil
}

// ReleaseIPAfter puts ip in quarantine until the given time: RequestIP does
// not return it before then, unless every other address in the network is
// taken. ip does not have to be allocated, so a restarted host can hold
// addresses that the previous process released. If ip is already held, the
// later deadline wins. A deadline that has passed releases ip at once.
func (a *IPAllocator) ReleaseIPAfter(network *net.IPNet, ip net.IP, until time.Time) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	nw := &net.IPNet{IP: network.IP.Mask(network.Mask), Mask: network.Mask}
	key := nw.String()
	allocated, ok := a.allocatedIPs[key]
	if !ok {
		allocated = newAllocatedMap(nw)
		a.allocatedIPs[key] = allocated
	}
	k := ip.String()
	if !until.After(a.now()) {
		delete(allocated.p, k)
		delete(allocated.held, k)
		return nil
	}
	pos := ipToBigInt(ip)
	if pos == nil || pos.Cmp(allocated.begin) == -1 || pos.Cmp(allocated.end) == 1 {
		return ErrIPOutOfRange
	}
	if prev, ok := allocated.held[k]; ok && prev.After(until) {
		return nil
	}
	allocated.p[k] = struct{}{}
	allocated.held[k] = until
	return nil
}

// expireHeld frees quarantined addresses whose deadline has passed.
func (allocated *allocatedMap) expireHeld(now time.Time) {
	for k, until := range allocated.held {
		if !until.After(now) {
			delete(allocated.p, k)
			delete(allocated.held, k)
		}
	}
}

// takeEarliestHeld hands out the quarantined address with the nearest
// deadline. It is the fallback when no address is free.
func (allocated *allocatedMap) takeEarliestHeld() (net.IP, bool) {
	var best string
	var bestUntil time.Time
	for k, until := range allocated.held {
		if best == "" || until.Before(bestUntil) || (until.Equal(bestUntil) && k < best) {
			best, bestUntil = k, until
		}
	}
	if best == "" {
		return nil, false
	}
	delete(allocated.held, best)
	ip := net.ParseIP(best)
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip, true
}

func (allocated *allocatedMap) checkIP(ip net.IP) (net.IP, error) {
	if _, ok := allocated.p[ip.String()]; ok {
		return nil, ErrIPAlreadyAllocated
	}

	pos := ipToBigInt(ip)
	// Verify that the IP address is within our network range.
	if pos.Cmp(allocated.begin) == -1 || pos.Cmp(allocated.end) == 1 {
		return nil, ErrIPOutOfRange
	}

	// Register the IP.
	allocated.p[ip.String()] = struct{}{}

	return ip, nil
}

// return an available ip if one is currently available.  If not,
// return the next available ip for the network
func (allocated *allocatedMap) getNextIP() (net.IP, error) {
	pos := big.NewInt(0).Set(allocated.last)
	allRange := big.NewInt(0).Sub(allocated.end, allocated.begin)
	for i := big.NewInt(0); i.Cmp(allRange) <= 0; i.Add(i, big.NewInt(1)) {
		pos.Add(pos, big.NewInt(1))
		if pos.Cmp(allocated.end) == 1 {
			pos.Set(allocated.begin)
		}
		if _, ok := allocated.p[bigIntToIP(pos).String()]; ok {
			continue
		}
		allocated.p[bigIntToIP(pos).String()] = struct{}{}
		allocated.last.Set(pos)
		return bigIntToIP(pos), nil
	}
	if ip, ok := allocated.takeEarliestHeld(); ok {
		return ip, nil
	}
	return nil, ErrNoAvailableIPs
}

// Converts a 4 bytes IP into a 128 bit integer
func ipToBigInt(ip net.IP) *big.Int {
	x := big.NewInt(0)
	if ip4 := ip.To4(); ip4 != nil {
		return x.SetBytes(ip4)
	}
	if ip6 := ip.To16(); ip6 != nil {
		return x.SetBytes(ip6)
	}

	return nil
}

// Converts 128 bit integer into a 4 bytes IP address
func bigIntToIP(v *big.Int) net.IP {
	return net.IP(v.Bytes())
}

// getIPCopy returns a copy of the passed IP address
func getIPCopy(from net.IP) net.IP {
	to := make(net.IP, len(from))
	copy(to, from)
	return to
}

// networkRange calculates the first and last IP addresses in an IPNet
func networkRange(network *net.IPNet) (net.IP, net.IP) {
	if network == nil {
		return nil, nil
	}

	firstIP := network.IP.Mask(network.Mask)
	lastIP := getIPCopy(firstIP)
	for i := 0; i < len(firstIP); i++ {
		lastIP[i] = firstIP[i] | ^network.Mask[i]
	}

	if network.IP.To4() != nil {
		firstIP = firstIP.To4()
		lastIP = lastIP.To4()
	}

	return firstIP, lastIP
}

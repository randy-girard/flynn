package httphelper

import (
	"net"
	"net/http"
	"strings"
)

func remoteIP(r *http.Request) (net.IP, string, bool) {
	if r == nil {
		return nil, "", false
	}
	addr := strings.TrimSpace(r.RemoteAddr)
	if addr == "" {
		return nil, "", false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip, addr, true
	}
	return nil, addr, false
}

// RequestFromLoopbackOrUnix reports whether r arrived from a TCP loopback
// address (127.0.0.0/8 or ::1) or a Unix domain socket. Spoofable forwarding
// headers (X-Forwarded-For, X-Real-IP) are ignored; only RemoteAddr counts.
// An empty or unparseable address fails closed (not local).
func RequestFromLoopbackOrUnix(r *http.Request) bool {
	ip, addr, parsed := remoteIP(r)
	if parsed {
		return ip.IsLoopback()
	}
	if addr == "" {
		return false
	}
	// Unix listeners typically set RemoteAddr to a filesystem path or an
	// abstract name ("@…"). There is no host:port to parse.
	return strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "@")
}

// RequestFromLocalMachine is true for loopback, Unix sockets, and TCP
// connections whose source IP is assigned to this host. flynn-host often
// listens on --listen-ip (not 127.0.0.1); discoverd, flannel, and flynn-builder
// notify that address from the same machine.
func RequestFromLocalMachine(r *http.Request) bool {
	if RequestFromLoopbackOrUnix(r) {
		return true
	}
	ip, _, parsed := remoteIP(r)
	if !parsed {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP == nil {
			continue
		}
		if n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

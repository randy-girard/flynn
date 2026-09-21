package httphelper

import (
	"net"
	"net/http"
	"strings"
)

// RequestFromLoopbackOrUnix reports whether r arrived from a TCP loopback
// address (127.0.0.0/8 or ::1) or a Unix domain socket. Spoofable forwarding
// headers (X-Forwarded-For, X-Real-IP) are ignored; only RemoteAddr counts.
// An empty or unparseable address fails closed (not local).
func RequestFromLoopbackOrUnix(r *http.Request) bool {
	if r == nil {
		return false
	}
	addr := strings.TrimSpace(r.RemoteAddr)
	if addr == "" {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// Unix listeners typically set RemoteAddr to a filesystem path or an
	// abstract name ("@…"). There is no host:port to parse.
	return strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "@")
}

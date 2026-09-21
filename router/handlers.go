package main

import (
	"net"
	"net/http"
	"strings"
)

const (
	fwdForHeaderName   = "X-Forwarded-For"
	fwdProtoHeaderName = "X-Forwarded-Proto"
	fwdPortHeaderName  = "X-Forwarded-Port"
)

// fwdProtoHandler records the hop Flynn observed on inbound requests.
//
// X-Forwarded-Proto and X-Forwarded-Port are overwritten unconditionally with
// Proto and Port (this listener's scheme and port). Client-supplied values are
// discarded so a request on the HTTP listener cannot claim it arrived over TLS.
//
// X-Forwarded-For appends the peer IP to any prior chain. Only the last
// element is the address Flynn observed; earlier hops are client-controlled
// and must not be trusted. The status service and request logger already
// read the last element.
type fwdProtoHandler struct {
	http.Handler
	Proto string
	Port  string
}

func (h fwdProtoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Append the observed peer IP. Prior X-Forwarded-For hops are retained
	// as a comma+space list so downstream services that already read the
	// last element keep working; only that last element is trustworthy.
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if prior, ok := r.Header[fwdForHeaderName]; ok {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		r.Header.Set(fwdForHeaderName, clientIP)
	}

	r.Header.Set(fwdProtoHeaderName, h.Proto)
	r.Header.Set(fwdPortHeaderName, h.Port)

	h.Handler.ServeHTTP(w, r)
}

package cli

import (
	"net"
	"net/http"
	"time"
)

const (
	controllerResponseHeaderTimeout = 15 * time.Second
	controllerRepairHTTPTimeout     = 20 * time.Second
)

// newControllerHTTPClient builds a controller HTTP client that resolves
// .discoverd names via dial. timeout=0 keeps the streaming deploy client
// unbounded; a positive timeout is for non-streaming repair RPCs only.
func newControllerHTTPClient(dial func(network, addr string) (net.Conn, error), timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Dial:                  dial,
			ResponseHeaderTimeout: controllerResponseHeaderTimeout,
			TLSHandshakeTimeout:   10 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

package cli

import (
	"context"
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
// DialContext (not Dial) so plugin waitHTTP's per-probe context can cancel a
// lookup or TCP connect to a draining instance after a plugin rebuild.
func newControllerHTTPClient(dial func(network, addr string) (net.Conn, error), timeout time.Duration) *http.Client {
	if dial == nil {
		dial = discoverdDial
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           contextDial(dial),
			ResponseHeaderTimeout: controllerResponseHeaderTimeout,
			TLSHandshakeTimeout:   10 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

func contextDial(dial func(network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		type dialRes struct {
			c   net.Conn
			err error
		}
		ch := make(chan dialRes, 1)
		go func() {
			c, err := dial(network, addr)
			ch <- dialRes{c, err}
		}()
		select {
		case <-ctx.Done():
			go func() {
				res := <-ch
				if res.c != nil {
					res.c.Close()
				}
			}()
			return nil, ctx.Err()
		case res := <-ch:
			return res.c, res.err
		}
	}
}

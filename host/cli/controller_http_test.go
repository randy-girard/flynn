package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestNewControllerHTTPClientStreamingHasNoTimeout(t *testing.T) {
	dial := func(network, addr string) (net.Conn, error) {
		return nil, net.ErrClosed
	}
	c := newControllerHTTPClient(dial, 0)
	if c.Timeout != 0 {
		t.Fatalf("streaming client Timeout=%s, want 0", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T", c.Transport)
	}
	if tr.ResponseHeaderTimeout != controllerResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout=%s", tr.ResponseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Fatal("DialContext must be set so waitHTTP can cancel a draining discoverd peer")
	}
}

func TestDiscoverdHTTPClientHasResponseHeaderTimeout(t *testing.T) {
	c := discoverdHTTPClient()
	if c.Timeout != 0 {
		t.Fatalf("Timeout=%s, want 0 for streaming and large uploads", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T", c.Transport)
	}
	if tr.ResponseHeaderTimeout != controllerResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout=%s", tr.ResponseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Fatal("DialContext must be set so waitHTTP can cancel a draining discoverd peer")
	}
}

func TestNewControllerHTTPClientRepairHasTimeout(t *testing.T) {
	c := newControllerHTTPClient(nil, controllerRepairHTTPTimeout)
	if c.Timeout != controllerRepairHTTPTimeout {
		t.Fatalf("repair Timeout=%s want %s", c.Timeout, controllerRepairHTTPTimeout)
	}
	if c.Timeout == 0 {
		t.Fatal("repair client must bound JobList/VolumeList")
	}
}

func TestContextDialHonorsCancel(t *testing.T) {
	started := make(chan struct{})
	dial := func(network, addr string) (net.Conn, error) {
		close(started)
		select {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	c, err := contextDial(dial)(ctx, "tcp", "127.0.0.1:1")
	if err == nil || !errors.Is(err, context.Canceled) {
		if c != nil {
			c.Close()
		}
		t.Fatalf("canceled dial: conn=%v err=%v", c, err)
	}
}

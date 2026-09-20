package cli

import (
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
	if tr.Dial == nil {
		t.Fatal("Dial must be set")
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

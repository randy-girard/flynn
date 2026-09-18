package cli

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestControllerClientUsesDiscoverdNameNotInstanceIP(t *testing.T) {
	src, err := os.ReadFile("controller_client.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, `"http://controller.discoverd"`) {
		t.Fatal("controllerClient must use http://controller.discoverd so HTTP and Hijack share discoverdDial")
	}
	if strings.Contains(body, `"http://"+instances[0].Addr`) {
		t.Fatal("controllerClient must not pin Hijack to a controller instance IP")
	}
	if !strings.Contains(body, "Dial: discoverdDial") {
		t.Fatal("discoverdHTTPClient must set Transport.Dial to discoverdDial")
	}
}

func TestDiscoverdDialPassthroughNumericAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}()
	c, err := discoverdDial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	<-done
}

func TestDiscoverdDialDoesNotUseSystemDNSForDiscoverdHost(t *testing.T) {
	_, err := discoverdDial("tcp", "controller.discoverd:80")
	if err == nil {
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "127.0.0.53") || strings.Contains(msg, ":53:") {
		t.Fatalf("discoverdDial used system DNS instead of the discoverd API: %v", err)
	}
}

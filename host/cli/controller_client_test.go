package cli

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestUpdaterDoesNotPinFirstDiscoverdAddr(t *testing.T) {
	for _, path := range []string{"controller_client.go", "github_updater.go", "acme.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "addr = addrs[0]") {
			t.Fatalf("%s pins the first discoverd address; rotate across instances", path)
		}
	}
}

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

func TestControllerClientUsesAPIKeyHelperAfterSEC028(t *testing.T) {
	for _, path := range []string{"controller_client.go", "github_updater.go", "acme.go", "events.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		if !strings.Contains(body, "controllerAPIKey") {
			t.Fatalf("%s must use controllerAPIKey so volume gc and update work after AUTH_KEY left discoverd meta", path)
		}
		if strings.Contains(body, "KeyFromEnvOrMeta(") {
			t.Fatalf("%s must not call KeyFromEnvOrMeta directly (empty discoverd meta 401s GET /volumes)", path)
		}
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

func TestRotateDiscoverdAddrsCyclesInstances(t *testing.T) {
	discoverdDialCursor = 0
	addrs := []string{"10.0.0.1:80", "10.0.0.2:80"}
	first := rotateDiscoverdAddrs(addrs)
	second := rotateDiscoverdAddrs(addrs)
	if len(first) != 2 || first[0] != "10.0.0.1:80" || first[1] != "10.0.0.2:80" {
		t.Fatalf("first rotation = %v", first)
	}
	if len(second) != 2 || second[0] != "10.0.0.2:80" || second[1] != "10.0.0.1:80" {
		t.Fatalf("second rotation = %v", second)
	}
}

func TestDiscoverdDialFailsOverWhenFirstInstanceRefuses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
		close(accepted)
	}()

	prev := lookupDiscoverdAddrs
	lookupDiscoverdAddrs = func(service string) ([]string, error) {
		if service != "controller" {
			t.Errorf("service=%s", service)
		}
		return []string{"127.0.0.1:1", ln.Addr().String()}, nil
	}
	defer func() { lookupDiscoverdAddrs = prev }()
	discoverdDialCursor = 0

	c, err := discoverdDial("tcp", "controller.discoverd:80")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	<-accepted
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

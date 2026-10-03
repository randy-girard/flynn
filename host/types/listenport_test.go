package host

import "testing"

func TestListenPortEnv(t *testing.T) {
	ports := []Port{{Port: 8080, Proto: "tcp"}, {Port: 0, Proto: "tcp"}}
	ApplyListenPorts(ports)
	if ports[0].Port != 8080 || ports[1].Port != DefaultListenPort+1 {
		t.Fatalf("ports=%v", ports)
	}
	env := ListenPortEnv(ports)
	if env["PORT"] != "8080" || env["PORT_0"] != "8080" || env["PORT_1"] != "5001" {
		t.Fatalf("env=%v", env)
	}
	if ListenPortEnv(nil) != nil {
		t.Fatal("no ports")
	}
	zero := []Port{{Port: 0, Proto: "tcp"}}
	ApplyListenPorts(zero)
	if zero[0].Port != DefaultListenPort {
		t.Fatalf("dynamic=%d", zero[0].Port)
	}
	if ListenPortEnv(zero)["PORT"] != "5000" {
		t.Fatalf("dynamic PORT=%v", ListenPortEnv(zero))
	}
}

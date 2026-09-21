package httphelper

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestFromLoopbackOrUnix(t *testing.T) {
	cases := []struct {
		name       string
		remote     string
		forwarded  string
		want       bool
		nilRequest bool
	}{
		{name: "nil request", nilRequest: true, want: false},
		{name: "empty RemoteAddr", remote: "", want: false},
		{name: "httptest default TEST-NET", remote: "192.0.2.1:1234", want: false},
		{name: "subnet IPv4", remote: "10.0.0.5:1113", want: false},
		{name: "link-local", remote: "169.254.1.1:9", want: false},
		{name: "IPv4 loopback", remote: "127.0.0.1:54321", want: true},
		{name: "IPv4 loopback block", remote: "127.0.0.2:9", want: true},
		{name: "IPv6 loopback", remote: "[::1]:9", want: true},
		{name: "IPv4-mapped loopback", remote: "[::ffff:127.0.0.1]:9", want: true},
		{name: "unix path", remote: "/var/run/flynn-host.sock", want: true},
		{name: "unix abstract", remote: "@flynn-host", want: true},
		{name: "hostname not trusted", remote: "localhost:1113", want: false},
		{
			name:      "X-Forwarded-For cannot spoof loopback",
			remote:    "198.51.100.9:9",
			forwarded: "127.0.0.1",
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.nilRequest {
				if RequestFromLoopbackOrUnix(nil) {
					t.Fatal("nil request must fail closed")
				}
				return
			}
			req := httptest.NewRequest(http.MethodGet, "/host/jobs", nil)
			req.RemoteAddr = tc.remote
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
				req.Header.Set("X-Real-IP", tc.forwarded)
			}
			if got := RequestFromLoopbackOrUnix(req); got != tc.want {
				t.Fatalf("RemoteAddr=%q forwarded=%q got %v want %v", tc.remote, tc.forwarded, got, tc.want)
			}
		})
	}
}

func TestRequestFromLocalMachine(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/host/jobs", nil)
	req.RemoteAddr = "10.0.0.5:1113"
	if RequestFromLocalMachine(req) {
		t.Fatal("foreign subnet IP must not count as local machine")
	}
	req.RemoteAddr = "127.0.0.1:9"
	if !RequestFromLocalMachine(req) {
		t.Fatal("loopback must count as local machine")
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var local net.IP
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP == nil || n.IP.IsLoopback() {
			continue
		}
		if n.IP.To4() == nil {
			continue
		}
		local = n.IP
		break
	}
	if local == nil {
		t.Skip("no non-loopback IPv4 on this host")
	}
	req.RemoteAddr = net.JoinHostPort(local.String(), "9")
	if !RequestFromLocalMachine(req) {
		t.Fatalf("own interface %s must count as local machine", local)
	}
}

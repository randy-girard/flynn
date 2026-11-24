//go:build linux

package proxy

import (
	"bytes"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

// TestBlackholedBackendFailsInBoundedTime drops every packet a backend sends
// (a vanished host: no ACK, no RST) and checks the router gives up on it in
// bounded time, with and without a request body. It needs iptables and
// CAP_NET_ADMIN and is skipped without them.
func TestBlackholedBackendFailsInBoundedTime(t *testing.T) {
	if err := exec.Command("iptables", "-w", "-L", "OUTPUT", "-n").Run(); err != nil {
		t.Skipf("needs iptables with CAP_NET_ADMIN: %v", err)
	}
	defer func(d time.Duration) { backendStreamDeadPeerTimeout = d }(backendStreamDeadPeerTimeout)
	backendStreamDeadPeerTimeout = 12 * time.Second

	for _, tc := range []struct {
		name string
		body io.Reader
		min  time.Duration
		max  time.Duration
	}{
		{name: "GET", min: 3 * time.Second, max: 10 * time.Second},
		{name: "POST", body: bytes.NewReader(make([]byte, slowBodySize)), min: 10 * time.Second, max: 20 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(ioutil.Discard, r.Body)
			}))
			defer backend.Close()
			p := newTestProxy(backend.Listener.Addr().String())
			srv := httptest.NewServer(p)
			defer srv.Close()

			// a pooled connection to the backend, made before it vanishes
			res, err := http.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()

			_, port, _ := net.SplitHostPort(backend.Listener.Addr().String())
			rule := []string{"OUTPUT", "-o", "lo", "-p", "tcp", "--sport", port, "-j", "DROP"}
			if out, err := exec.Command("iptables", append([]string{"-w", "-I"}, rule...)...).CombinedOutput(); err != nil {
				t.Fatalf("iptables: %v: %s", err, out)
			}
			defer exec.Command("iptables", append([]string{"-w", "-D"}, rule...)...).Run()

			method := "GET"
			if tc.body != nil {
				method = "POST"
			}
			req, _ := http.NewRequest(method, srv.URL, tc.body)
			start := time.Now()
			res, err = (&http.Client{Timeout: time.Minute}).Do(req)
			took := time.Since(start)
			if err != nil {
				t.Fatalf("after %v: %v, want the router's 503", took, err)
			}
			res.Body.Close()
			if res.StatusCode != 503 || took < tc.min || took > tc.max {
				t.Fatalf("status %d after %v, want 503 after %v to %v", res.StatusCode, took.Round(100*time.Millisecond), tc.min, tc.max)
			}
		})
	}
}

package httpclient

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawReqAcceptsNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	if _, err := c.RawReq("GET", "/", nil, nil, nil); err != nil {
		t.Fatalf("expected 204 with nil out to succeed: %v", err)
	}
	if _, err := c.RawReq("POST", "/", nil, nil, nil); err != nil {
		t.Fatalf("expected 204 POST with nil out to succeed: %v", err)
	}
}

func TestRawReqNoContentWithBodyStillErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	var out map[string]string
	if _, err := c.RawReq("GET", "/", nil, nil, &out); err == nil {
		t.Fatal("expected error decoding 204 into out")
	}
}

func hijackServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Upgrade", "flynn-attach/0")
		w.WriteHeader(http.StatusSwitchingProtocols)
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		bufrw.WriteString("ok\n")
		bufrw.Flush()
		conn.Close()
	}))
}

func TestHijackUsesTransportDial(t *testing.T) {
	srv := hijackServer(t)
	defer srv.Close()

	var gotAddr string
	c := &Client{
		URL: "http://controller.discoverd",
		HTTP: &http.Client{Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				gotAddr = addr
				return net.Dial(network, strings.TrimPrefix(srv.URL, "http://"))
			},
		}},
	}
	rwc, err := c.Hijack("GET", "/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rwc.Close()
	if gotAddr != "controller.discoverd:80" {
		t.Fatalf("Hijack dialed %q, want controller.discoverd:80 via Transport.Dial", gotAddr)
	}
	b, err := io.ReadAll(rwc)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "ok\n" {
		t.Fatalf("body %q", b)
	}
}

func TestHijackPrefersHijackDialOverTransportDial(t *testing.T) {
	srv := hijackServer(t)
	defer srv.Close()

	var transportUsed, hijackUsed bool
	target := strings.TrimPrefix(srv.URL, "http://")
	c := &Client{
		URL: "http://controller.discoverd",
		HTTP: &http.Client{Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				transportUsed = true
				return net.Dial(network, target)
			},
		}},
		HijackDial: func(network, addr string) (net.Conn, error) {
			hijackUsed = true
			return net.Dial(network, target)
		},
	}
	rwc, err := c.Hijack("GET", "/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rwc.Close()
	if !hijackUsed {
		t.Fatal("expected HijackDial")
	}
	if transportUsed {
		t.Fatal("HijackDial must win over Transport.Dial (pinned TLS)")
	}
}

package proxy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	router "github.com/randy-girard/flynn/router/types"
)

func newStickyTestProxy(addrs ...string) *ReverseProxy {
	var key [32]byte
	copy(key[:], "sticky-test-key-sticky-test-key!")
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	return NewReverseProxy(ReverseProxyConfig{
		BackendListFunc: func() []*router.Backend {
			backends := make([]*router.Backend, len(addrs))
			for i, a := range addrs {
				backends[i] = &router.Backend{Service: "test", Addr: a}
			}
			return backends
		},
		StickyKey:      &key,
		Sticky:         true,
		RequestTracker: nopTracker{},
		Logger:         l,
		Service:        "test",
	})
}

func stickySetCookie(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == StickyCookieName {
			if found != nil {
				t.Fatalf("more than one %s cookie set: %v", StickyCookieName, res.Header["Set-Cookie"])
			}
			found = c
		}
	}
	if found == nil {
		t.Fatalf("no %s cookie set: %v", StickyCookieName, res.Header["Set-Cookie"])
	}
	return found
}

// The sticky cookie is never readable by page script, is only sent back over
// TLS when it was set over TLS, and is not sent on cross-site subrequests.
// X-Forwarded-Proto is the listener's own scheme: the router's fwdProtoHandler
// overwrites whatever the client sent.
func TestStickyCookieAttributes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", "websocket")
			w.WriteHeader(http.StatusSwitchingProtocols)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	p := newStickyTestProxy(srv.Listener.Addr().String())
	front := httptest.NewServer(p)
	defer front.Close()
	defer p.Close()

	for _, upgrade := range []bool{false, true} {
		for _, proto := range []string{"http", "https", ""} {
			req, _ := http.NewRequest("GET", front.URL+"/", nil)
			if proto != "" {
				req.Header.Set("X-Forwarded-Proto", proto)
			}
			if upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			res, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			name := "proto=" + proto
			if upgrade {
				name += " upgrade"
			}
			c := stickySetCookie(t, res)
			if !c.HttpOnly {
				t.Errorf("%s: cookie not HttpOnly: %s", name, res.Header.Get("Set-Cookie"))
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("%s: SameSite = %v, want Lax: %s", name, c.SameSite, res.Header.Get("Set-Cookie"))
			}
			if want := proto == "https"; c.Secure != want {
				t.Errorf("%s: Secure = %v, want %v: %s", name, c.Secure, want, res.Header.Get("Set-Cookie"))
			}
			if c.Path != "/" {
				t.Errorf("%s: Path = %q, want /", name, c.Path)
			}
		}
	}
}

// A cookie set before the attributes were added carries the same encrypted
// value, so it still pins the client to its backend after a router update.
func TestStickyCookieFromOlderRouterStillPins(t *testing.T) {
	var hits [2]int
	var srvs [2]*httptest.Server
	for i := range srvs {
		i := i
		srvs[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits[i]++
		}))
		defer srvs[i].Close()
	}
	p := newStickyTestProxy(srvs[0].Listener.Addr().String(), srvs[1].Listener.Addr().String())
	defer p.Close()

	// what the router wrote before: Name, Value and Path only
	old := (&http.Cookie{
		Name:  StickyCookieName,
		Value: base64.StdEncoding.EncodeToString(encrypt([]byte(srvs[1].Listener.Addr().String()), *p.transport.stickyCookieKey)),
		Path:  "/",
	}).String()
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("GET", "http://example.com/", nil)
		req.Header.Set("Cookie", strings.SplitN(old, ";", 2)[0])
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if got := rec.Header().Get("Set-Cookie"); got != "" {
			t.Fatalf("request %d: pinned request got a new cookie: %s", i, got)
		}
	}
	if hits[0] != 0 || hits[1] != 20 {
		t.Fatalf("hits = %v, want all 20 on backend 1", hits)
	}
}

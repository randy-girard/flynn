package main

import (
	"net/http"
	"net/http/httptest"

	. "github.com/flynn/go-check"
)

var nopHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

func (s *S) TestFwdProtoHandlerSetsObservedHop(c *C) {
	rec := httptest.NewRecorder()
	request, _ := http.NewRequest("GET", "http://test.com", nil)
	request.RemoteAddr = "1.2.3.4:5678"
	h := fwdProtoHandler{Handler: nopHandler, Proto: "https", Port: "443"}
	h.ServeHTTP(rec, request)
	c.Assert(request.Header.Get("X-Forwarded-For"), Equals, "1.2.3.4")
	c.Assert(request.Header.Get("X-Forwarded-Proto"), Equals, "https")
	c.Assert(request.Header.Get("X-Forwarded-Port"), Equals, "443")
}

func (s *S) TestFwdProtoHandlerOverwritesClientProtoAndPort(c *C) {
	// SEC-025: a client on HTTP can send X-Forwarded-Proto: https. Flynn
	// must replace it with the hop it observed so backends that read the
	// first element cannot be tricked into treating the request as TLS.
	rec := httptest.NewRecorder()
	request, _ := http.NewRequest("GET", "http://test.com", nil)
	request.RemoteAddr = "1.2.3.4:5678"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Add("X-Forwarded-Proto", "also-https")
	request.Header.Set("X-Forwarded-Port", "443")
	h := fwdProtoHandler{Handler: nopHandler, Proto: "http", Port: "80"}
	h.ServeHTTP(rec, request)
	c.Assert(request.Header.Get("X-Forwarded-Proto"), Equals, "http")
	c.Assert(request.Header.Get("X-Forwarded-Port"), Equals, "80")
	c.Assert(request.Header["X-Forwarded-Proto"], DeepEquals, []string{"http"})
	c.Assert(request.Header["X-Forwarded-Port"], DeepEquals, []string{"80"})
}

func (s *S) TestFwdProtoHandlerAppendsPeerToForwardedFor(c *C) {
	// Only the last X-Forwarded-For element is the address Flynn observed.
	// Prior hops are client-controlled; status and the request logger
	// already read the last element.
	rec := httptest.NewRecorder()
	request, _ := http.NewRequest("GET", "http://test.com", nil)
	request.RemoteAddr = "10.0.0.9:1234"
	request.Header.Set("X-Forwarded-For", "192.168.1.1")
	request.Header.Add("X-Forwarded-For", "10.1.1.1")
	h := fwdProtoHandler{Handler: nopHandler, Proto: "http", Port: "80"}
	h.ServeHTTP(rec, request)
	c.Assert(request.Header.Get("X-Forwarded-For"), Equals, "192.168.1.1, 10.1.1.1, 10.0.0.9")
}

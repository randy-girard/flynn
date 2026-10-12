package main

import (
	"net"
	"net/http"
	"time"

	. "github.com/flynn/go-check"
	"golang.org/x/sys/unix"
)

func (S) TestLayerTransportTimeouts(c *C) {
	tr := newLayerTransport(net.Dial)
	c.Assert(tr.Dial, NotNil)
	c.Assert(tr.ResponseHeaderTimeout, Equals, layerResponseHeaderTimeout)
	c.Assert(tr.IdleConnTimeout, Equals, layerIdleConnTimeout)
	c.Assert(tr.ResponseHeaderTimeout > 0, Equals, true)
	c.Assert(layerTCPUserTimeout > 0, Equals, true)
}

func (S) TestLayerDialerSetsTCPUserTimeout(c *C) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	defer ln.Close()

	conn, err := newLayerDialer(1500*time.Millisecond).Dial("tcp", ln.Addr().String())
	c.Assert(err, IsNil)
	defer conn.Close()

	raw, err := conn.(*net.TCPConn).SyscallConn()
	c.Assert(err, IsNil)
	var got int
	var sockErr error
	c.Assert(raw.Control(func(fd uintptr) {
		got, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
	}), IsNil)
	c.Assert(sockErr, IsNil)
	c.Assert(got, Equals, 1500)
}

// A layer server that accepts the request but never answers (as a pooled
// connection to a vanished host behaves) must fail the download quickly.
func (S) TestLayerTransportFailsOnStalledServer(c *C) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	orig := layerResponseHeaderTimeout
	layerResponseHeaderTimeout = 200 * time.Millisecond
	defer func() { layerResponseHeaderTimeout = orig }()

	client := &http.Client{Transport: newLayerTransport(net.Dial)}
	start := time.Now()
	_, err = client.Get("http://" + ln.Addr().String() + "/layer.squashfs")
	c.Assert(err, NotNil)
	c.Assert(time.Since(start) < 5*time.Second, Equals, true)
}

func (S) TestParseTagArgs(c *C) {
	type test struct {
		args     string
		expected map[string]string
	}
	for _, t := range []*test{
		{
			args:     "",
			expected: map[string]string{},
		},
		{
			args:     "foo",
			expected: map[string]string{"foo": "true"},
		},
		{
			args:     "foo,",
			expected: map[string]string{"foo": "true"},
		},
		{
			args:     "foo=",
			expected: map[string]string{"foo": ""},
		},
		{
			args:     "foo=bar",
			expected: map[string]string{"foo": "bar"},
		},
		{
			args:     "foo=bar,",
			expected: map[string]string{"foo": "bar"},
		},
		{
			args:     "foo=bar=baz",
			expected: map[string]string{"foo": "bar=baz"},
		},
		{
			args:     "foo=bar,baz",
			expected: map[string]string{"foo": "bar", "baz": "true"},
		},
		{
			args:     "foo=bar,baz=",
			expected: map[string]string{"foo": "bar", "baz": ""},
		},
		{
			args:     "foo=bar,baz=,",
			expected: map[string]string{"foo": "bar", "baz": ""},
		},
		{
			args:     "foo=bar,baz=qux",
			expected: map[string]string{"foo": "bar", "baz": "qux"},
		},
	} {
		actual := parseTagArgs(t.args)
		c.Assert(actual, DeepEquals, t.expected, Commentf("parsing %q", t.args))
	}
}

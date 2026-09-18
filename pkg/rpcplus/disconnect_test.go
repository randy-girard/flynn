package rpcplus

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIsBenignRPCDisconnect(t *testing.T) {
	unixEOF := &net.OpError{Op: "read", Net: "unix", Addr: nil, Err: io.EOF}
	unixEOF.Addr = &net.UnixAddr{Name: "@/.container-shared/rpc.sock", Net: "unix"}
	cases := []struct {
		err   error
		want  bool
		label string
	}{
		{nil, true, "nil"},
		{io.EOF, true, "eof"},
		{io.ErrUnexpectedEOF, true, "unexpected eof"},
		{net.ErrClosed, true, "closed"},
		{unixEOF, true, "unix wrapped eof"},
		{errors.New("read unix @->/.container-shared/rpc.sock: EOF"), true, "logged unix eof"},
		{errors.New("gob: decode error"), false, "real protocol error"},
		{errors.New("rpc: client protocol error: unexpected type"), false, "non-eof protocol"},
	}
	for _, tc := range cases {
		if got := isBenignRPCDisconnect(tc.err); got != tc.want {
			t.Errorf("%s: isBenignRPCDisconnect(%v) = %v, want %v", tc.label, tc.err, got, tc.want)
		}
	}
}

func TestUnixSocketCloseDoesNotLogProtocolError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
		c.Close()
	}()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	prevFlags := log.Flags()
	log.SetFlags(0)
	defer log.SetFlags(prevFlags)

	client, err := Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	_ = client.Close()

	out := buf.String()
	if strings.Contains(out, "rpc: client protocol error") {
		t.Fatalf("peer-close EOF must not log protocol error, got %q", out)
	}
}

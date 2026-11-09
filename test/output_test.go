package main

import "testing"

func TestCommandOutputKeepsRealLines(t *testing.T) {
	in := "flynn env get is now flynn env:get\nBAZ\n"
	if got := commandOutput(in); got != "BAZ\n" {
		t.Fatalf("commandOutput(%q) = %q", in, got)
	}
	if got := commandOutput("\n"); got != "\n" {
		t.Fatalf("blank line = %q", got)
	}
	if got := commandOutput("hello world\n"); got != "hello world\n" {
		t.Fatalf("plain = %q", got)
	}
	if got := firstOutputLine("flynn route:add is now flynn route:add\nhttp/abc\nCreate a CNAME\n"); got != "http/abc" {
		t.Fatalf("first line = %q", got)
	}
}

func TestTCPRoutePort(t *testing.T) {
	out := "tcp/abc listening on port 9999\nThe cluster firewall will open TCP 9999.\n"
	if got := tcpRoutePort(out); got != "9999" {
		t.Fatalf("port = %q", got)
	}
	if got := tcpRoutePort("flynn route:add is now flynn route:add\n" + out); got != "9999" {
		t.Fatalf("banner port = %q", got)
	}
	if got := tcpRoutePort("9999\nThe cluster firewall will open TCP 9999.\n"); got != "9999" {
		t.Fatalf("split across the firewall sentence = %q", got)
	}
}

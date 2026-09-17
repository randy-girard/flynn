package main

import "testing"

func TestLeadingVersionFlag(t *testing.T) {
	if !leadingVersionFlag([]string{"--version"}) {
		t.Fatal("flynn --version")
	}
	if !leadingVersionFlag([]string{"--help", "--version"}) {
		t.Fatal("flynn --help --version")
	}
	if !leadingVersionFlag([]string{"-h", "--version"}) {
		t.Fatal("flynn -h --version")
	}
	if leadingVersionFlag(nil) {
		t.Fatal("no args")
	}
	if leadingVersionFlag([]string{"version"}) {
		t.Fatal("version subcommand is not the global flag")
	}
	if leadingVersionFlag([]string{"apps"}) {
		t.Fatal("other commands")
	}
}

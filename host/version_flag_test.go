package main

import "testing"

func TestLeadingVersionFlag(t *testing.T) {
	if !leadingVersionFlag([]string{"--version"}) {
		t.Fatal("flynn-host --version")
	}
	if !leadingVersionFlag([]string{"-h", "--version"}) {
		t.Fatal("flynn-host -h --version")
	}
	if leadingVersionFlag([]string{"version"}) {
		t.Fatal("version subcommand is not the global flag")
	}
	if leadingVersionFlag([]string{"ps"}) {
		t.Fatal("other commands")
	}
}

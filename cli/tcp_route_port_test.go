package main

import (
	"strings"
	"testing"
)

func TestRouteAddTCPRejectsPortOutsideWindow(t *testing.T) {
	args := parseCLI(t, []string{"route:add", "tcp", "-p", "22"})
	err := runRouteAddTCP(args, nil)
	if err == nil || !strings.Contains(err.Error(), "10000-29999") {
		t.Fatalf("port 22: %v", err)
	}
	args = parseCLI(t, []string{"route:add", "tcp", "-p", "3001"})
	err = runRouteAddTCP(args, nil)
	if err == nil || !strings.Contains(err.Error(), "10000-29999") {
		t.Fatalf("port 3001: %v", err)
	}
	args = parseCLI(t, []string{"route:add", "tcp", "-p", "5002"})
	err = runRouteAddTCP(args, nil)
	if err == nil || !strings.Contains(err.Error(), "10000-29999") {
		t.Fatalf("port 5002: %v", err)
	}
}

func TestAddonsExposeRejectsPortOutsideWindow(t *testing.T) {
	args := parseCLI(t, []string{"addons:expose", "postgres", "-p", "1111"})
	args.String["<resource>"] = "postgres"
	err := runResourceExpose(args, nil)
	if err == nil || !strings.Contains(err.Error(), "10000-29999") {
		t.Fatalf("addons:expose -p 1111: %v", err)
	}
}

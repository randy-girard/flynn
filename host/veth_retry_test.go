package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/opencontainers/runc/libcontainer/configs"
)

func TestIsNetworkIfaceExistsErr(t *testing.T) {
	err := errors.New(`container_linux.go:346: starting container process caused "process_linux.go:393: creating network interfaces caused \"file exists\""`)
	if !isNetworkIfaceExistsErr(err) {
		t.Fatal("expected match")
	}
	if isNetworkIfaceExistsErr(errors.New("apply apparmor profile")) {
		t.Fatal("apparmor is not a veth EEXIST")
	}
	if isNetworkIfaceExistsErr(nil) {
		t.Fatal("nil")
	}
}

func TestRegenerateVethHostNames(t *testing.T) {
	cfg := &configs.Config{
		Networks: []*configs.Network{
			{Type: "loopback", HostInterfaceName: ""},
			{Type: "veth", HostInterfaceName: "vethdead"},
			{Type: "veth", HostInterfaceName: "vethold2"},
		},
	}
	var deleted []string
	if err := regenerateVethHostNamesWith(cfg, func(name string) { deleted = append(deleted, name) }); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 || deleted[0] != "vethdead" || deleted[1] != "vethold2" {
		t.Fatalf("deleted %q", deleted)
	}
	if cfg.Networks[0].HostInterfaceName != "" {
		t.Fatalf("loopback renamed %q", cfg.Networks[0].HostInterfaceName)
	}
	for _, n := range cfg.Networks[1:] {
		if n.HostInterfaceName == "" || n.HostInterfaceName == "vethdead" || n.HostInterfaceName == "vethold2" {
			t.Fatalf("veth not regenerated: %q", n.HostInterfaceName)
		}
		if !strings.HasPrefix(n.HostInterfaceName, "veth") {
			t.Fatalf("prefix %q", n.HostInterfaceName)
		}
	}
	if cfg.Networks[1].HostInterfaceName == cfg.Networks[2].HostInterfaceName {
		t.Fatal("both veths got the same name")
	}
}

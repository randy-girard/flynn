package cli

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/placement"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func TestPlacementModeRegisteredAndAliased(t *testing.T) {
	if commands["placement:mode"] == nil {
		t.Fatal("missing placement:mode")
	}
	name, rest, from := ResolveCommand("placement", []string{"mode"})
	if name != "placement:mode" || len(rest) != 0 || from != "placement mode" {
		t.Fatalf("alias %s %v %s", name, rest, from)
	}
	name, rest, from = ResolveCommand("placement", []string{"mode", "split"})
	if name != "placement:mode" || len(rest) != 1 || rest[0] != "split" {
		t.Fatalf("alias with mode %s %v %s", name, rest, from)
	}
}

func TestPlacementModeBodyIsModeOnly(t *testing.T) {
	body := placementModeBody("split")
	if len(body) != 1 || body["mode"] != "split" {
		t.Fatalf("%v", body)
	}
}

func TestPlacementPendingWhenNoAppHost(t *testing.T) {
	core := cluster.NewHost("core", "127.0.0.1:1113", nil, map[string]string{placement.TagRole: placement.RoleCore})
	down := cluster.NewHost("down", "127.0.0.1:1113", nil, map[string]string{placement.TagRole: placement.RoleApp})
	down.SetShuttingDown(true)
	msg, err := placementPendingAppHosts(nil)
	if err != nil || msg != "" {
		t.Fatalf("nil client %q %v", msg, err)
	}
	if msg := placementPendingMessage([]*cluster.Host{core, down, nil}); !strings.Contains(msg, "stay pending") || !strings.Contains(msg, "left running") {
		t.Fatalf("pending message %q", msg)
	}
	app := cluster.NewHost("app", "127.0.0.1:1113", nil, map[string]string{placement.TagRole: placement.RoleApp})
	if msg := placementPendingMessage([]*cluster.Host{core, app}); msg != "" {
		t.Fatalf("app host should not warn: %q", msg)
	}
}

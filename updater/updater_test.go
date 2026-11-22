package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

type failingHostLister struct{ calls int }

func (f *failingHostLister) Hosts() ([]*cluster.Host, error) {
	f.calls++
	return nil, errors.New("discoverd: connection refused")
}

func fastHostListRetry(t *testing.T) {
	t.Helper()
	oldA, oldD := updaterdeploy.HostListAttempts, updaterdeploy.HostListDelay
	updaterdeploy.HostListAttempts, updaterdeploy.HostListDelay = 3, time.Millisecond
	t.Cleanup(func() { updaterdeploy.HostListAttempts, updaterdeploy.HostListDelay = oldA, oldD })
}

func discardLog() log15.Logger {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	return l
}

// One transient discoverd error used to skip the discoverd/flannel swap and
// let `update` exit 0 with both apps on the old image.
func TestUpdateFailsWhenHostListFails(t *testing.T) {
	fastHostListRetry(t)
	lister := &failingHostLister{}
	replaced := false
	err := replaceHostInfra(lister, func([]*cluster.Host) error {
		replaced = true
		return nil
	}, discardLog())
	if err == nil {
		t.Fatal("want the update to fail when the host list keeps failing")
	}
	if replaced {
		t.Fatal("host-infra replace must not run without a host list")
	}
	if lister.calls != 3 {
		t.Fatalf("want the host list retried 3 times, got %d", lister.calls)
	}
}

func TestReplaceHostInfraPassesListedHosts(t *testing.T) {
	fastHostListRetry(t)
	want := []*cluster.Host{
		cluster.NewHost("host1", "http://10.0.0.1:1113", nil, nil),
		cluster.NewHost("host2", "http://10.0.0.2:1113", nil, nil),
	}
	var got []*cluster.Host
	err := replaceHostInfra(staticHostLister(want), func(h []*cluster.Host) error {
		got = h
		return nil
	}, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 hosts passed to the replace step, got %d", len(got))
	}
}

func TestReplaceHostInfraReturnsReplaceError(t *testing.T) {
	fastHostListRetry(t)
	hosts := []*cluster.Host{cluster.NewHost("host1", "http://10.0.0.1:1113", nil, nil)}
	err := replaceHostInfra(staticHostLister(hosts), func([]*cluster.Host) error {
		return errors.New("swap failed")
	}, discardLog())
	if err == nil || !strings.Contains(err.Error(), "swap failed") {
		t.Fatalf("want the replace error, got %v", err)
	}
}

type staticHostLister []*cluster.Host

func (s staticHostLister) Hosts() ([]*cluster.Host, error) { return s, nil }

type strategyRecordingClient struct {
	controller.Client
	updated []string
}

func (c *strategyRecordingClient) UpdateApp(app *ct.App) error {
	c.updated = append(c.updated, app.Strategy)
	return nil
}

func (c *strategyRecordingClient) GetAppRelease(string) (*ct.Release, error) {
	return nil, errors.New("stop after strategy")
}

// The old `hostCount = 1` fallback put the controller on the single-node
// all-at-once strategy on a multi-node cluster.
func TestDeployAppFailsWhenHostListFails(t *testing.T) {
	fastHostListRetry(t)
	lister := &failingHostLister{}
	old := newHostLister
	newHostLister = func() updaterdeploy.HostLister { return lister }
	t.Cleanup(func() { newHostLister = old })

	client := &strategyRecordingClient{}
	app := &ct.App{Name: "controller", Strategy: ct.ControllerHAStrategy, Meta: map[string]string{"flynn-system-app": "true"}}
	err := deployApp(client, app, &ct.Artifact{}, nil, discardLog())
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("want deployApp to return the host list error, got %v", err)
	}
	if len(client.updated) != 0 {
		t.Fatalf("controller strategy must not change without a host count, got %v", client.updated)
	}
	if app.Strategy != ct.ControllerHAStrategy {
		t.Fatalf("app strategy changed to %q", app.Strategy)
	}
}

func TestDeployAppKeepsHAStrategyOnMultiNode(t *testing.T) {
	fastHostListRetry(t)
	old := newHostLister
	newHostLister = func() updaterdeploy.HostLister {
		return staticHostLister{
			cluster.NewHost("host1", "http://10.0.0.1:1113", nil, nil),
			cluster.NewHost("host2", "http://10.0.0.2:1113", nil, nil),
		}
	}
	t.Cleanup(func() { newHostLister = old })

	client := &strategyRecordingClient{}
	app := &ct.App{Name: "controller", Strategy: ct.ControllerHAStrategy, Meta: map[string]string{"flynn-system-app": "true"}}
	_ = deployApp(client, app, &ct.Artifact{}, nil, discardLog())
	if len(client.updated) != 0 || app.Strategy != ct.ControllerHAStrategy {
		t.Fatalf("multi-node controller must keep %q, got %q (updates %v)", ct.ControllerHAStrategy, app.Strategy, client.updated)
	}
}

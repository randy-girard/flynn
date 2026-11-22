package cli

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

type failingUpdateHostLister struct{ calls int }

func (f *failingUpdateHostLister) Hosts() ([]*cluster.Host, error) {
	f.calls++
	return nil, errors.New("discoverd: connection refused")
}

type strategyRecordingController struct {
	controller.Client
	updated []string
}

func (c *strategyRecordingController) UpdateApp(app *ct.App) error {
	c.updated = append(c.updated, app.Strategy)
	return nil
}

func (c *strategyRecordingController) GetAppRelease(string) (*ct.Release, error) {
	return nil, errors.New("stop after strategy")
}

// The old `hostCount = 1` fallback moved the controller to the single-node
// all-at-once strategy on a multi-node cluster when discoverd blinked.
func TestTarballDeployAppFailsWhenHostListFails(t *testing.T) {
	oldA, oldD := updaterdeploy.HostListAttempts, updaterdeploy.HostListDelay
	updaterdeploy.HostListAttempts, updaterdeploy.HostListDelay = 3, time.Millisecond
	t.Cleanup(func() { updaterdeploy.HostListAttempts, updaterdeploy.HostListDelay = oldA, oldD })
	lister := &failingUpdateHostLister{}
	oldL := updateHostLister
	updateHostLister = func() updaterdeploy.HostLister { return lister }
	t.Cleanup(func() { updateHostLister = oldL })

	log := log15.New()
	log.SetHandler(log15.DiscardHandler())
	client := &strategyRecordingController{}
	app := &ct.App{
		Name:          "controller",
		Strategy:      ct.ControllerHAStrategy,
		DeployTimeout: int32(minSystemDeployTimeout / time.Second),
		Meta:          map[string]string{"flynn-system-app": "true"},
	}
	err := deployApp(client, app, &ct.Artifact{}, nil, nil, false, log)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("want deployApp to return the host list error, got %v", err)
	}
	if len(client.updated) != 0 || app.Strategy != ct.ControllerHAStrategy {
		t.Fatalf("controller strategy must not change without a host count, got %q (updates %v)", app.Strategy, client.updated)
	}
	if lister.calls != 3 {
		t.Fatalf("want 3 host list attempts, got %d", lister.calls)
	}
}

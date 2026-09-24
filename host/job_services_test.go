package main

import (
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	host "github.com/randy-girard/flynn/host/types"
)

func TestHostRegistersServices(t *testing.T) {
	user := &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}}
	if !hostRegistersServices(user) {
		t.Fatal("user web jobs must be registered by flynn-host")
	}
	sys := &host.Job{Partition: "system", Metadata: map[string]string{"flynn-system-app": "true"}}
	if hostRegistersServices(sys) {
		t.Fatal("system jobs keep in-container discoverd registration")
	}
	build := &host.Job{Metadata: map[string]string{"flynn-controller.type": "dockerbuilder"}}
	if hostRegistersServices(build) {
		t.Fatal("build jobs are not user HTTP backends")
	}
}

func TestJobServiceInstance(t *testing.T) {
	inst := jobServiceInstance(map[string]string{
		"FLYNN_APP_NAME": "shop",
		"OTHER":          "nope",
	}, "100.64.1.9", host.Port{Port: 8080, Proto: "tcp"})
	if inst.Addr != "100.64.1.9:8080" || inst.Proto != "tcp" {
		t.Fatalf("%+v", inst)
	}
	if inst.Meta["FLYNN_APP_NAME"] != "shop" {
		t.Fatalf("meta=%v", inst.Meta)
	}
	if _, ok := inst.Meta["OTHER"]; ok {
		t.Fatal("non-instance env must not become discoverd meta")
	}
	if _, ok := discoverd.EnvInstanceMeta["FLYNN_APP_NAME"]; !ok {
		t.Fatal("sanity: FLYNN_APP_NAME is instance meta")
	}
}

func TestRegisterJobServicesWaitsForDiscoverd(t *testing.T) {
	l := &LibcontainerBackend{discoverdConfigured: make(chan struct{})}
	c := &Container{
		l: l,
		job: &host.Job{
			Metadata: map[string]string{
				"flynn-controller.app_name": "shop",
				"flynn-controller.type":     "web",
			},
			Config: host.ContainerConfig{Env: map[string]string{"EXTERNAL_IP": "10.0.0.1"}},
		},
	}
	done := make(chan error, 1)
	go func() { done <- c.registerJobServices(log15.New()) }()
	select {
	case err := <-done:
		t.Fatalf("returned before discoverd configured: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	close(l.discoverdConfigured)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after discoverdConfigured")
	}
}

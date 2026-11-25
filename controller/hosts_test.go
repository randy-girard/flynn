package main

import "testing"

type hostInfoStub struct {
	id       string
	shutdown bool
}

func (h hostInfoStub) ID() string              { return h.id }
func (h hostInfoStub) Tags() map[string]string { return map[string]string{"flynn-max-processes": "4"} }
func (h hostInfoStub) Addr() string            { return h.id + ":1113" }
func (h hostInfoStub) ShuttingDown() bool      { return h.shutdown }

// GET /hosts says which hosts drain, so the deployer's omni roll does not
// wait for them (EDGE-42).
func TestHostInfoCarriesShutdown(t *testing.T) {
	if info := hostInfoFrom(hostInfoStub{id: "h1", shutdown: true}); !info.Shutdown || info.ID != "h1" {
		t.Fatalf("draining host: %+v", info)
	}
	info := hostInfoFrom(hostInfoStub{id: "h2"})
	if info.Shutdown {
		t.Fatalf("live host marked shutdown: %+v", info)
	}
	if info.MaxProcesses == nil || *info.MaxProcesses != 4 {
		t.Fatalf("max processes lost: %+v", info)
	}
}

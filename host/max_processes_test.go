package main

import (
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/config"
	host "github.com/randy-girard/flynn/host/types"
)

func TestUpdateTagsMergesCapAndPersistsHostJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	conf := config.New()
	conf.Args = []string{"--peer-ips", "10.0.0.1", "--tags", "role=database"}
	if err := conf.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLYNN_HOST_CONFIG", path)

	h := &Host{
		status:  &host.HostStatus{Tags: map[string]string{"role": "database"}},
		discMan: NewDiscoverdManager(nil, nil, "h1", "10.0.0.1:1113", map[string]string{"role": "database"}),
	}
	err := h.UpdateTags(map[string]string{
		ct.FormationMaxProcessesTag:       "4",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceOperator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.status.Tags["role"] != "database" || h.status.Tags[ct.FormationMaxProcessesTag] != "4" {
		t.Fatalf("status tags %v", h.status.Tags)
	}
	got, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(got.Args, " ")
	if !strings.Contains(text, "--peer-ips") || !strings.Contains(text, "role=database") || !strings.Contains(text, ct.FormationMaxProcessesTag+"=4") {
		t.Fatalf("host.json args %v", got.Args)
	}
	if err := h.UpdateTags(map[string]string{ct.FormationMaxProcessesTag: ""}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.status.Tags[ct.FormationMaxProcessesTag]; ok {
		t.Fatalf("empty cap must be deleted: %v", h.status.Tags)
	}
	if h.status.Tags["role"] != "database" {
		t.Fatalf("role dropped: %v", h.status.Tags)
	}
}

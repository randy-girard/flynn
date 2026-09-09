package updaterdeploy

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/inconshreveable/log15"
)

func TestRepairDeposedSireniaPeers_SkipsAbsentPeers(t *testing.T) {
	meta, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "p"}},
		Sync:    &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "s"}},
		Deposed: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "gone"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &fakeDiscoverdService{
		meta: &discoverd.ServiceMeta{Data: meta},
		instances: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "p"}},
			{Meta: map[string]string{"POSTGRES_ID": "s"}},
		},
	}
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		if name != "postgres" {
			return &fakeDiscoverdService{metaErr: errors.New("not found")}
		}
		return svc
	}

	RepairDeposedSireniaPeers(log15.New())
	if len(svc.setMetas) != 0 {
		t.Fatalf("absent deposed peers should not trigger SetMeta, got %d writes", len(svc.setMetas))
	}
}

func TestRepairDeposedSireniaPeers_ClearsPresentPeers(t *testing.T) {
	origWait := deposedRejoinWait
	origPoll := deposedRejoinPollInterval
	defer func() {
		deposedRejoinWait = origWait
		deposedRejoinPollInterval = origPoll
	}()
	deposedRejoinWait = 500 * time.Millisecond
	deposedRejoinPollInterval = 20 * time.Millisecond

	broken, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "p"}},
		Sync:    &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "s"}},
		Deposed: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "async1"}},
			{Meta: map[string]string{"POSTGRES_ID": "gone"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "p"}},
		Sync:    &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "s"}},
		Async: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "async1"}},
		},
		Deposed: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "gone"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := &fakeDiscoverdService{
		metas: []*discoverd.ServiceMeta{
			{Data: broken},
			{Data: recovered},
		},
		instances: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "p"}},
			{Meta: map[string]string{"POSTGRES_ID": "s"}},
			{Meta: map[string]string{"POSTGRES_ID": "async1"}},
		},
	}
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		if name != "postgres" {
			return &fakeDiscoverdService{metaErr: errors.New("not found")}
		}
		return svc
	}

	RepairDeposedSireniaPeers(log15.New())
	if len(svc.setMetas) != 1 {
		t.Fatalf("expected one SetMeta, got %d", len(svc.setMetas))
	}
	var wrote sirenia.State
	if err := json.Unmarshal(svc.setMetas[0].Data, &wrote); err != nil {
		t.Fatal(err)
	}
	if len(wrote.Deposed) != 1 || peerApplianceID(wrote.Deposed[0]) != "gone" {
		t.Fatalf("expected only absent peer kept in Deposed, got %+v", wrote.Deposed)
	}
	if len(wrote.Async) != 0 {
		t.Fatalf("SetMeta should not invent asyncs, got %+v", wrote.Async)
	}
}

func TestPeerApplianceID(t *testing.T) {
	if got := peerApplianceID(nil); got != "" {
		t.Fatalf("nil -> %q", got)
	}
	if got := peerApplianceID(&discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "a"}}); got != "a" {
		t.Fatalf("postgres id -> %q", got)
	}
	if got := peerApplianceID(&discoverd.Instance{Meta: map[string]string{"MARIADB_ID": "b"}}); got != "b" {
		t.Fatalf("mariadb id -> %q", got)
	}
}

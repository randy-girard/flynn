package updaterdeploy

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	sirenia "github.com/randy-girard/flynn/pkg/sirenia/state"
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

func TestWaitForClearedDeposedInAsync_TimesOutWithoutError(t *testing.T) {
	origWait := deposedRejoinWait
	origPoll := deposedRejoinPollInterval
	defer func() {
		deposedRejoinWait = origWait
		deposedRejoinPollInterval = origPoll
	}()
	deposedRejoinWait = 40 * time.Millisecond
	deposedRejoinPollInterval = 10 * time.Millisecond

	meta, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "p"}},
		Sync:    &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &fakeDiscoverdService{meta: &discoverd.ServiceMeta{Data: meta}}
	if err := waitForClearedDeposedInAsync(svc, []string{"async1"}, log15.New()); err != nil {
		t.Fatalf("timeout must not fail the repair: %v", err)
	}
}

func TestWaitForClearedDeposedInAsync_PartialRejoinKeepsWaiting(t *testing.T) {
	origWait := deposedRejoinWait
	origPoll := deposedRejoinPollInterval
	defer func() {
		deposedRejoinWait = origWait
		deposedRejoinPollInterval = origPoll
	}()
	deposedRejoinWait = 50 * time.Millisecond
	deposedRejoinPollInterval = 10 * time.Millisecond

	partial, err := json.Marshal(sirenia.State{
		Async: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "async1"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &fakeDiscoverdService{metas: []*discoverd.ServiceMeta{{Data: partial}}}
	if err := waitForClearedDeposedInAsync(svc, []string{"async1", "async2"}, log15.New()); err != nil {
		t.Fatalf("partial rejoin must time out with nil: %v", err)
	}
	if svc.metaCalls < 2 {
		t.Fatalf("expected repeated GetMeta while waiting, got %d", svc.metaCalls)
	}
}

func TestWaitForClearedDeposedInAsync_SucceedsWhenAllRejoin(t *testing.T) {
	origWait := deposedRejoinWait
	origPoll := deposedRejoinPollInterval
	defer func() {
		deposedRejoinWait = origWait
		deposedRejoinPollInterval = origPoll
	}()
	deposedRejoinWait = time.Second
	deposedRejoinPollInterval = 5 * time.Millisecond

	empty, err := json.Marshal(sirenia.State{})
	if err != nil {
		t.Fatal(err)
	}
	rejoined, err := json.Marshal(sirenia.State{
		Async: []*discoverd.Instance{
			{Meta: map[string]string{"POSTGRES_ID": "async1"}},
			{Meta: map[string]string{"POSTGRES_ID": "async2"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &fakeDiscoverdService{
		metas: []*discoverd.ServiceMeta{
			{Data: empty},
			{Data: rejoined},
		},
	}
	if err := waitForClearedDeposedInAsync(svc, []string{"async1", "async2"}, log15.New()); err != nil {
		t.Fatal(err)
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

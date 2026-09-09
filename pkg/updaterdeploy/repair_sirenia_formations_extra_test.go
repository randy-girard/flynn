package updaterdeploy

import (
	"encoding/json"
	"errors"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/inconshreveable/log15"
)

func TestRepairOrphanSireniaFormationsNoopWhenNoOrphans(t *testing.T) {
	const (
		appID   = "postgres-app"
		release = "active-release"
	)
	meta, err := json.Marshal(struct {
		Primary *discoverd.Instance `json:"Primary"`
	}{
		Primary: &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": release}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		return &fakeDiscoverdService{meta: &discoverd.ServiceMeta{Data: meta}}
	}
	ctrl := &fakeRepairController{
		apps: []*ct.App{{ID: appID, Name: "postgres"}},
		releases: map[string]*ct.Release{
			release: {ID: release, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {{AppID: appID, ReleaseID: release, Processes: map[string]int{"postgres": 3}}},
		},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 0 {
		t.Fatalf("expected no formation updates, got %d", len(ctrl.puts))
	}
}

func TestRepairOrphanSireniaFormationsSkipsMissingMeta(t *testing.T) {
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		return &fakeDiscoverdService{metaErr: errors.New("not found")}
	}
	ctrl := &fakeRepairController{
		apps: []*ct.App{{ID: "postgres-app", Name: "postgres"}},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 0 {
		t.Fatal("expected no updates when meta missing")
	}
}

func TestRepairOrphanSireniaFormationsSkipsMissingApp(t *testing.T) {
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		t.Fatalf("should not look up discoverd for missing app %s", name)
		return nil
	}
	ctrl := &fakeRepairController{
		apps: []*ct.App{{ID: "other", Name: "redis"}},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
}

func TestRepairOrphanSireniaFormationsIgnoresAlreadyZeroOrphans(t *testing.T) {
	const (
		appID         = "postgres-app"
		activeRelease = "active-release"
		orphanRelease = "orphan-release"
	)
	meta, err := json.Marshal(struct {
		Primary *discoverd.Instance `json:"Primary"`
	}{
		Primary: &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": activeRelease}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		return &fakeDiscoverdService{meta: &discoverd.ServiceMeta{Data: meta}}
	}
	ctrl := &fakeRepairController{
		apps: []*ct.App{{ID: appID, Name: "postgres"}},
		releases: map[string]*ct.Release{
			activeRelease: {ID: activeRelease, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: activeRelease, Processes: map[string]int{"postgres": 3}},
				{AppID: appID, ReleaseID: orphanRelease, Processes: map[string]int{"postgres": 0}},
			},
		},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 0 {
		t.Fatalf("already-zero orphan should not be put, got %d", len(ctrl.puts))
	}
}

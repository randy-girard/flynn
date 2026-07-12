package updaterdeploy

import (
	"encoding/json"
	"testing"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/inconshreveable/log15"
)

type fakeRepairController struct {
	controller.Client
	apps       []*ct.App
	releases   map[string]*ct.Release
	formations map[string][]*ct.Formation
	puts       []*ct.Formation
}

func (f *fakeRepairController) AppList() ([]*ct.App, error) {
	return f.apps, nil
}

func (f *fakeRepairController) GetRelease(id string) (*ct.Release, error) {
	if r, ok := f.releases[id]; ok {
		return r, nil
	}
	return nil, controller.ErrNotFound
}

func (f *fakeRepairController) FormationList(appID string) ([]*ct.Formation, error) {
	return f.formations[appID], nil
}

func (f *fakeRepairController) PutFormation(formation *ct.Formation) error {
	f.puts = append(f.puts, formation)
	return nil
}

func TestRepairOrphanSireniaFormationsScalesStaleRelease(t *testing.T) {
	const (
		appID         = "postgres-app"
		activeRelease = "active-release"
		orphanRelease = "orphan-release"
	)

	primary := &discoverd.Instance{
		Meta: map[string]string{
			"FLYNN_RELEASE_ID": activeRelease,
			"POSTGRES_ID":      "primary",
		},
	}
	meta, err := json.Marshal(struct {
		Primary *discoverd.Instance `json:"Primary"`
		Sync    *discoverd.Instance `json:"Sync"`
		Async   []*discoverd.Instance `json:"Async"`
	}{
		Primary: primary,
		Sync:    &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "sync"}},
		Async:   []*discoverd.Instance{{Meta: map[string]string{"POSTGRES_ID": "async"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	origNewService := discoverdNewService
	defer func() { discoverdNewService = origNewService }()
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
				{AppID: appID, ReleaseID: orphanRelease, Processes: map[string]int{"postgres": 1}},
			},
		},
	}

	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 1 {
		t.Fatalf("expected 1 formation update, got %d", len(ctrl.puts))
	}
	if ctrl.puts[0].ReleaseID != orphanRelease {
		t.Fatalf("expected orphan release scaled, got %s", ctrl.puts[0].ReleaseID)
	}
	if ctrl.puts[0].Processes["postgres"] != 0 {
		t.Fatalf("expected postgres scaled to 0, got %d", ctrl.puts[0].Processes["postgres"])
	}
}

type fakeDiscoverdService struct {
	meta *discoverd.ServiceMeta
}

func (f *fakeDiscoverdService) GetMeta() (*discoverd.ServiceMeta, error) {
	return f.meta, nil
}

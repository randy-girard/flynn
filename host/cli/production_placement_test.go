package cli

import (
	"errors"
	"testing"

	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func roleTaggedHost(id, role string) *cluster.Host {
	tags := map[string]string{ct.FormationMaxProcessesTag: "4"}
	if role != "" {
		tags[placement.TagRole] = role
	}
	return cluster.NewHost(id, "127.0.0.1:1113", nil, tags)
}

func TestProductionCheckPlacementMixedIgnoresRole(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		env:     map[string]string{"DEFAULT_BACKEND": "s3main", "BACKEND_S3MAIN": "backend=s3"},
		jobs: []placementJob{{
			HostTags: map[string]string{},
			AppID:    "webapp",
			AppName:  "webapp",
			Type:     "web",
		}},
	}
	got, err := f.run(t)
	assertPassed(t, err)
	row := mustCheck(t, got, "ok", "placement", "mode=mixed")
	if row.action == "" {
		t.Fatal("missing action")
	}
}

func TestProductionCheckPlacementSplit(t *testing.T) {
	s3 := map[string]string{"DEFAULT_BACKEND": "s3main", "BACKEND_S3MAIN": "backend=s3"}
	core := roleTaggedHost("core", placement.RoleCore)
	app := roleTaggedHost("app", placement.RoleApp)
	downCore := roleTaggedHost("down", placement.RoleCore)
	downCore.SetShuttingDown(true)

	t.Run("no core fails", func(t *testing.T) {
		f := &productionFixture{
			tenancy:   ct.TenancySettings{Mode: "self_hosted"},
			env:       s3,
			placement: ct.WorkloadPlacement{Mode: "split"},
			hosts:     []*cluster.Host{app, downCore},
		}
		got, err := f.run(t)
		assertFailed(t, err)
		mustCheck(t, got, "fail", "placement", "mode=split core=0 app=1")
	})

	t.Run("no app warns", func(t *testing.T) {
		f := &productionFixture{
			tenancy:   ct.TenancySettings{Mode: "self_hosted"},
			env:       s3,
			placement: ct.WorkloadPlacement{Mode: "split"},
			hosts:     []*cluster.Host{core},
		}
		got, err := f.run(t)
		assertPassed(t, err)
		mustCheck(t, got, "warn", "placement", "mode=split core=1 app=0")
	})

	t.Run("wrong role names the app and the router stays quiet", func(t *testing.T) {
		f := &productionFixture{
			tenancy:   ct.TenancySettings{Mode: "self_hosted"},
			env:       s3,
			placement: ct.WorkloadPlacement{Mode: "split"},
			hosts:     []*cluster.Host{core, app},
			procs: map[string]ct.ProcessType{
				"web": {},
				"app": {Omni: true},
			},
			jobs: []placementJob{
				{
					HostTags: core.Tags(),
					AppID:    "webapp",
					AppName:  "webapp",
					Type:     "web",
				},
				{
					HostTags: app.Tags(),
					AppID:    "router",
					AppName:  "router",
					Type:     "app",
					Meta:     map[string]string{"flynn-system-app": "true"},
				},
				{
					HostTags: app.Tags(),
					AppID:    "controller",
					AppName:  "controller",
					Type:     "web",
					Meta:     map[string]string{"flynn-system-app": "true"},
				},
			},
		}
		got, err := f.run(t)
		assertPassed(t, err)
		mustCheck(t, got, "warn", "placement", "mode=split core=1 app=1 mismatch=controller,webapp")
	})

	t.Run("read error fails", func(t *testing.T) {
		f := &productionFixture{
			tenancy:      ct.TenancySettings{Mode: "self_hosted"},
			env:          s3,
			placementErr: errors.New("placement down"),
		}
		got, err := f.run(t)
		assertFailed(t, err)
		mustCheck(t, got, "fail", "placement", "error=placement down")
	})

	t.Run("job list error fails", func(t *testing.T) {
		f := &productionFixture{
			tenancy:   ct.TenancySettings{Mode: "self_hosted"},
			env:       s3,
			placement: ct.WorkloadPlacement{Mode: "mixed"},
			jobsErr:   errors.New("jobs down"),
		}
		got, err := f.run(t)
		assertFailed(t, err)
		mustCheck(t, got, "fail", "placement", "error=jobs down")
	})

	t.Run("role tag is not flynn-role", func(t *testing.T) {
		f := &productionFixture{
			tenancy:   ct.TenancySettings{Mode: "self_hosted"},
			env:       s3,
			placement: ct.WorkloadPlacement{Mode: "split"},
			hosts: []*cluster.Host{cluster.NewHost("h", "127.0.0.1:1113", nil, map[string]string{
				"role":                      "app",
				ct.FormationMaxProcessesTag: "4",
			})},
		}
		got, err := f.run(t)
		assertFailed(t, err)
		mustCheck(t, got, "fail", "placement", "mode=split core=0 app=0")
	})
}

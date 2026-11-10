package main

import (
	"math"
	"strings"
	"testing"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	tu "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
)

func mibPtr(n int64) *int64 {
	v := n * 1024 * 1024
	return &v
}

func memType(limit *int64) ct.ProcessType {
	if limit == nil {
		return ct.ProcessType{}
	}
	return ct.ProcessType{Resources: resource.Resources{
		resource.TypeMemory: {Limit: limit},
	}}
}

func memFormation(owner, appID, releaseID string, procs map[string]int, types map[string]ct.ProcessType) memoryFormation {
	return memoryFormation{
		Owner: owner, AppID: appID, ReleaseID: releaseID,
		Processes: procs, Types: types,
	}
}

func TestMemoryHostMatchesIgnoresProcessCapSource(t *testing.T) {
	tags := map[string]string{
		"disk":                            "ssd",
		ct.FormationMaxProcessesTag:       "9",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceOperator,
	}
	host := memoryHost{ID: "h1", Tags: map[string]string{
		"disk":                            "ssd",
		ct.FormationMaxProcessesTag:       "4",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	}}
	if !memoryHostMatches(tags, host) {
		t.Fatal("process cap tags must not change which hosts count")
	}
}

func TestQuotaEmptyOwnerSkips(t *testing.T) {
	api := &controllerAPI{tenancy: &data.TenancyRepo{}}
	if scaleQuotaApplies(api, &ct.App{}) {
		t.Fatal("empty OwnerAccount must skip the memory quota")
	}
	if scaleQuotaApplies(api, nil) {
		t.Fatal("nil app must skip the memory quota")
	}
	if !scaleQuotaApplies(api, &ct.App{OwnerAccount: "user:a"}) {
		t.Fatal("owned app must be checked")
	}
	if scaleQuotaApplies(&controllerAPI{}, &ct.App{OwnerAccount: "user:a"}) {
		t.Fatal("missing tenancy repo must skip")
	}
	if scaleQuotaApplies(nil, &ct.App{OwnerAccount: "user:a"}) {
		t.Fatal("nil controller must skip")
	}
}

func TestQuotaPartialProcessMapKeepsOmittedTypes(t *testing.T) {
	stored := map[string]int{"web": 1, "worker": 1}
	req := map[string]int{"web": 2}
	got := overlayProcessCounts(stored, req)
	if got["web"] != 2 || got["worker"] != 1 {
		t.Fatalf("overlay = %v, want web 2 and worker 1", got)
	}
	forms := []memoryFormation{
		memFormation("user:a", "app", "rel", got, map[string]ct.ProcessType{
			"web":    memType(mibPtr(256)),
			"worker": memType(mibPtr(256)),
		}),
	}
	mb, err := accountMemoryMB("user:a", forms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 768 {
		t.Fatalf("memory = %d MB, want 768 (omitted worker still running)", mb)
	}
}

func TestQuotaInternalCountsRestored(t *testing.T) {
	stored := map[string]int{"web": 1, "slugbuilder": 1}
	incoming := map[string]int{"web": 2, "slugbuilder": 9}
	got := lockInternalProcessCounts(stored, incoming)
	if got["slugbuilder"] != 1 || got["web"] != 2 {
		t.Fatalf("locked counts = %v", got)
	}
	mb, err := accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", got, map[string]ct.ProcessType{
			"web":         memType(mibPtr(256)),
			"slugbuilder": memType(mibPtr(128)),
		}),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 640 {
		t.Fatalf("memory = %d MB, want 640 (slugbuilder stays 1)", mb)
	}
}

func TestQuotaNonCurrentReleaseStaysInMemory(t *testing.T) {
	current := memFormation("user:a", "app", "new", map[string]int{"web": 1}, map[string]ct.ProcessType{
		"web": memType(mibPtr(256)),
	})
	old := memFormation("user:a", "app", "old", map[string]int{"web": 1}, map[string]ct.ProcessType{
		"web": memType(mibPtr(256)),
	})
	scaled := old
	scaled.Processes = overlayProcessCounts(old.Processes, map[string]int{"web": 2})
	forms := projectFormations([]memoryFormation{current, old}, scaled)
	mb, err := accountMemoryMB("user:a", forms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 768 {
		t.Fatalf("memory = %d MB, want 768 (current release plus the release being scaled)", mb)
	}
}

func TestQuotaCreateScaleOverBudget(t *testing.T) {
	// CreateScale overlays the request onto the stored formation, including
	// a release that is not apps.release_id, then the same budget applies.
	current := memFormation("user:a", "app", "current", map[string]int{"web": 1}, map[string]ct.ProcessType{
		"web": memType(mibPtr(256)),
	})
	target := memFormation("user:a", "app", "target", map[string]int{"web": 1, "worker": 1}, map[string]ct.ProcessType{
		"web":    memType(mibPtr(256)),
		"worker": memType(mibPtr(256)),
	})
	other := memFormation("user:b", "other", "rel", map[string]int{"web": 5}, map[string]ct.ProcessType{
		"web": memType(mibPtr(256)),
	})
	target.Processes = overlayProcessCounts(target.Processes, map[string]int{"web": 3})
	forms := projectFormations([]memoryFormation{current, target, other}, target)
	mb, err := accountMemoryMB("user:a", forms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 1280 {
		t.Fatalf("memory = %d MB, want 1280", mb)
	}
	limit := 512
	if err := memoryAllows(&limit, mb); err == nil {
		t.Fatal("CreateScale over the stored budget must be rejected")
	}
}

func TestQuotaEqualAllowedAndNextMBRejected(t *testing.T) {
	limit := 512
	if err := memoryAllows(&limit, 512); err != nil {
		t.Fatalf("equal to the limit must be allowed: %v", err)
	}
	if err := memoryAllows(&limit, 513); err == nil {
		t.Fatal("one MB over the limit must be rejected")
	}
	if err := memoryAllows(&limit, 0); err != nil {
		t.Fatalf("zero memory must be allowed: %v", err)
	}
}

func TestQuotaCeilIsWholeMB(t *testing.T) {
	oneByte := int64(1)
	forms := []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
			"web": memType(&oneByte),
		}),
	}
	mb, err := accountMemoryMB("user:a", forms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 1 {
		t.Fatalf("1 byte = %d MB, want 1", mb)
	}
	limit := 1
	if err := memoryAllows(&limit, mb); err != nil {
		t.Fatal("a 1-byte limit ceils to 1 MB and is still allowed at a 1 MB quota")
	}
	plus := int64(1024*1024 + 1)
	mb, err = accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
			"web": memType(&plus),
		}),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 2 {
		t.Fatalf("1 MiB + 1 byte = %d MB, want 2", mb)
	}
}

func TestQuotaNilAndZeroLimitsRejected(t *testing.T) {
	req := int64(1024 * 1024 * 1024)
	_, err := accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
			"web": {Resources: resource.Resources{resource.TypeMemory: {Request: &req}}},
		}),
	}, nil, nil)
	if err == nil {
		t.Fatal("nil Limit must be rejected even when Request is set")
	}
	zero := int64(0)
	_, err = accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
			"web": memType(&zero),
		}),
	}, nil, nil)
	if err == nil {
		t.Fatal("Limit 0 must be rejected")
	}
	_, err = accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{}),
	}, nil, nil)
	if err == nil {
		t.Fatal("missing process spec must be rejected")
	}
}

func TestQuotaStoppedTypeWithNilLimitIgnored(t *testing.T) {
	mb, err := accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": 1, "worker": 0}, map[string]ct.ProcessType{
			"web": memType(mibPtr(256)),
		}),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 256 {
		t.Fatalf("memory = %d, want 256", mb)
	}
}

func TestQuotaOverflowRejected(t *testing.T) {
	_, err := accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:a", "app", "rel", map[string]int{"web": math.MaxInt64}, map[string]ct.ProcessType{
			"web": memType(mibPtr(2)),
		}),
	}, nil, nil)
	if err == nil {
		t.Fatal("overflowing process memory must be rejected")
	}
}

func TestQuotaScaleDownStillOverAndScaleToZero(t *testing.T) {
	if tenancy.ScaleIncreases(map[string]int{"web": 4}, map[string]int{"web": 3}) {
		t.Fatal("web 4 to 3 is not a scale increase; memory is still checked")
	}
	limit := 512
	// 3 × 256 MB is still over 512 after a scale-down from 4.
	if err := memoryAllows(&limit, 768); err == nil {
		t.Fatal("scale-down that is still over the limit must be rejected")
	}
	current := memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
		"web": memType(mibPtr(256)),
	})
	sibling := memFormation("user:a", "other", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
		"web": memType(mibPtr(128)),
	})
	zeroed := current
	zeroed.Processes = map[string]int{"web": 0}
	forms := projectFormations([]memoryFormation{current, sibling}, zeroed)
	mb, err := accountMemoryMB("user:a", forms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 128 {
		t.Fatalf("scaling one formation to zero = %d MB, want the sibling's 128", mb)
	}
}

func TestQuotaOmniMatchingHosts(t *testing.T) {
	limit := mibPtr(128)
	types := map[string]ct.ProcessType{
		"web": {Omni: true, Resources: resource.Resources{resource.TypeMemory: {Limit: limit}}},
	}
	base := memFormation("user:a", "app", "rel", map[string]int{"web": 2}, types)
	hosts := []memoryHost{
		{ID: "host1", Tags: map[string]string{"disk": "ssd"}},
		{ID: "host2", Tags: map[string]string{"disk": "ssd"}},
		{ID: "host3", Tags: map[string]string{"disk": "hdd"}, Shutdown: true},
	}
	base.Tags = map[string]map[string]string{"web": {ct.FormationHostIDsTag: "host1,host3"}}
	mb, err := accountMemoryMB("user:a", []memoryFormation{base}, hosts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 256 {
		t.Fatalf("omni memory = %d MB, want 256 (2 jobs × the one matching live host × 128)", mb)
	}

	pinned := base
	pinned.Tags = map[string]map[string]string{
		"web": {ct.FormationHostIDsTag: "host1", ct.FormationAvoidHostIDsTag: "host1"},
	}
	mb, err = accountMemoryMB("user:a", []memoryFormation{pinned}, hosts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 0 {
		t.Fatalf("avoided host memory = %d, want 0", mb)
	}

	profiled := memFormation("user:a", "app", "rel", map[string]int{"web": 1}, map[string]ct.ProcessType{
		"web": {
			Omni:           true,
			RuntimeProfile: "Dedicated",
			Resources:      resource.Resources{resource.TypeMemory: {Limit: mibPtr(100)}},
		},
	})
	mb, err = accountMemoryMB("user:a", []memoryFormation{profiled}, []memoryHost{
		{ID: "h1", Tags: map[string]string{"role": "database"}},
		{ID: "h2", Tags: map[string]string{"role": "web"}},
	}, map[string]map[string]string{"dedicated": {"role": "database"}})
	if err != nil {
		t.Fatal(err)
	}
	if mb != 100 {
		t.Fatalf("runtime-profile omni memory = %d, want 100", mb)
	}
}

func TestQuotaIgnoresOtherOwners(t *testing.T) {
	mb, err := accountMemoryMB("user:a", []memoryFormation{
		memFormation("user:b", "b", "rel", map[string]int{"web": 4}, map[string]ct.ProcessType{
			"web": memType(mibPtr(256)),
		}),
		memFormation("", "system", "rel", map[string]int{"web": 4}, map[string]ct.ProcessType{
			"web": memType(mibPtr(256)),
		}),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 0 {
		t.Fatalf("other owners = %d, want 0", mb)
	}
}

func TestQuotaOneOffRunJob(t *testing.T) {
	mb, err := oneOffMemoryMB("myapp", "runner", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 1024 {
		t.Fatalf("default one-off = %d MB, want 1024 (1 GiB from SetDefaults)", mb)
	}
	mb, err = oneOffMemoryMB("myapp", "runner", resource.Resources{
		resource.TypeMemory: {Limit: mibPtr(256)},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 256 {
		t.Fatalf("explicit one-off = %d MB, want 256", mb)
	}
	_, err = oneOffMemoryMB("myapp", "runner", resource.Resources{
		resource.TypeMemory: {Limit: mibPtr(0)},
	}, true)
	if err == nil {
		t.Fatal("one-off Limit 0 must be rejected")
	}
}

func TestQuotaSlugbuilderOneOff(t *testing.T) {
	mb, err := oneOffMemoryMB("myapp", "slugbuilder", resource.Resources{
		resource.TypeMemory: {Limit: mibPtr(256)},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 12*1024 {
		t.Fatalf("slugbuilder = %d MB, want 12288 (12 GiB)", mb)
	}
	mb, err = oneOffMemoryMB("builder", "runner", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 12*1024 {
		t.Fatalf("builder app = %d MB, want 12288", mb)
	}
	mb, err = oneOffMemoryMB("myapp", "dockerbuilder", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if mb != 12*1024 {
		t.Fatalf("dockerbuilder = %d MB, want 12288", mb)
	}
	_, err = oneOffMemoryMB("myapp", "slugbuilder", nil, false)
	if err == nil {
		t.Fatal("slugbuilder must be rejected when the host cannot set the build cap")
	}
	if !(&controllerAPI{}).buildJobMemoryAvailable() {
		t.Fatal("shipped flynn-host unit sets Delegate=yes, so the default build cap is on")
	}
}

func TestQuotaStoredRowNotFreeTier(t *testing.T) {
	stored := 256
	limits, err := tenancy.EffectiveLimits(tenancy.ModeHosted, &tenancy.Limits{MaxMemoryMB: &stored})
	if err != nil {
		t.Fatal(err)
	}
	if limits.MaxMemoryMB == nil || *limits.MaxMemoryMB != 256 {
		t.Fatalf("stored quota = %+v, want 256 not the hosted free tier", limits.MaxMemoryMB)
	}
	if err := memoryAllows(limits.MaxMemoryMB, 256); err != nil {
		t.Fatal(err)
	}
	if err := memoryAllows(limits.MaxMemoryMB, 257); err == nil {
		t.Fatal("257 MB must exceed the stored 256 MB row")
	}

	// A stored row with NULL max_memory_mb is unlimited, including in hosted mode.
	explicit, err := tenancy.EffectiveLimits(tenancy.ModeHosted, &tenancy.Limits{MaxApps: &stored})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.MaxMemoryMB != nil {
		t.Fatal("NULL max_memory_mb on a stored row must stay unlimited")
	}
	if err := memoryAllows(explicit.MaxMemoryMB, 100000); err != nil {
		t.Fatal(err)
	}

	free, err := tenancy.EffectiveLimits(tenancy.ModeHosted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if free.MaxMemoryMB == nil || *free.MaxMemoryMB != tenancy.HostedMaxMemoryMB {
		t.Fatalf("no row in hosted mode = %+v, want %d", free.MaxMemoryMB, tenancy.HostedMaxMemoryMB)
	}

	self, err := tenancy.EffectiveLimits(tenancy.ModeSelfHosted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if self.MaxMemoryMB != nil {
		t.Fatal("self-hosted with no row must be unlimited")
	}
	if err := memoryAllows(self.MaxMemoryMB, 100000); err != nil {
		t.Fatal(err)
	}
}

func (s *S) TestHostedMemoryQuotaRejectsScale(c *C) {
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	defer func() {
		s.api.buildJobMemoryLimits = nil
		c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	}()

	unowned := s.createTestApp(c, &ct.App{Name: "quota-unowned"})
	unownedRel := s.createTestRelease(c, unowned.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: unowned.ID, ReleaseID: unownedRel.ID, Processes: map[string]int{"web": 1},
	}), IsNil)

	app := s.createTestApp(c, &ct.App{Name: "quota-scale", OwnerAccount: "user:quota-scale"})
	settings, err := s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(settings.Mode, Equals, tenancy.ModeHosted)
	rawApp, err := s.api.appRepo.Get(app.ID)
	c.Assert(err, IsNil)
	c.Assert(rawApp.(*ct.App).OwnerAccount, Equals, "user:quota-scale")
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}, "worker": {}},
	})
	rawRel, err := s.api.releaseRepo.Get(release.ID)
	c.Assert(err, IsNil)
	// A new process type with no memory of its own is stored as the small
	// runtime, 512 MiB. Two copies are 1024 MB, over the hosted free tier.
	web := rawRel.(*ct.Release).Processes["web"]
	c.Assert(web.Resources[resource.TypeMemory].Limit, NotNil)
	c.Assert(*web.Resources[resource.TypeMemory].Limit, Equals, int64(512*1024*1024))
	mem, err := s.api.projectedScaleMemory(rawApp.(*ct.App), rawRel.(*ct.Release), map[string]int{"web": 2}, nil, true)
	c.Assert(err, IsNil)
	c.Assert(mem, Equals, int64(1024))
	put := func(procs map[string]int) error {
		return s.c.PutFormation(&ct.Formation{
			AppID: app.ID, ReleaseID: release.ID, Processes: procs,
		})
	}
	err = put(map[string]int{"web": 2})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	mb := 1024
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: app.OwnerAccount, MaxMemoryMB: &mb}), IsNil)
	c.Assert(put(map[string]int{"web": 2}), IsNil)

	err = put(map[string]int{"worker": 1})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	low := 512
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: app.OwnerAccount, MaxMemoryMB: &low}), IsNil)
	err = put(map[string]int{"web": 2})
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)
	c.Assert(put(map[string]int{"web": 0, "worker": 0}), IsNil)

	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: app.OwnerAccount, MaxMemoryMB: &mb}), IsNil)
	c.Assert(put(map[string]int{"web": 1}), IsNil)
	old := s.createTestRelease(c, app.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	err = s.c.PutFormation(&ct.Formation{
		AppID: app.ID, ReleaseID: old.ID, Processes: map[string]int{"web": 2},
	})
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	g := &grpcAPI{controllerAPI: s.api}
	c.Assert(g.enforceCreateScale(app.ID, old.ID, map[string]int{"web": 1}, nil, false), IsNil)
	err = g.enforceCreateScale(app.ID, old.ID, map[string]int{"web": 2}, nil, false)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	c.Assert(put(map[string]int{"web": 0}), IsNil)
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: app.OwnerAccount, MaxMemoryMB: &low}), IsNil)
	s.cc.AddHost(tu.NewFakeHostClient(fakeHostID(), false))
	_, err = s.c.RunJobDetached(app.ID, &ct.NewJob{ReleaseID: release.ID, Args: []string{"echo"}})
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	build := 12 * 1024
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: app.OwnerAccount, MaxMemoryMB: &build}), IsNil)
	// Compiles are scheduler jobs. A one-off cannot use a builder type even
	// when the account quota would have covered the old 12 GiB build charge.
	_, err = s.c.RunJobDetached(app.ID, &ct.NewJob{ReleaseID: release.ID, Type: "slugbuilder", Args: []string{"echo"}})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "not permitted"), Equals, true)
	off := false
	s.api.buildJobMemoryLimits = &off
	_, err = s.c.RunJobDetached(app.ID, &ct.NewJob{ReleaseID: release.ID, Type: "dockerbuilder", Args: []string{"echo"}})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "not permitted"), Equals, true)

	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: app.OwnerAccount}), IsNil)
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 2},
	}), IsNil)
}

package main

import (
	"io"
	"net/http"
	"strings"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
)

func oneMiBProcess() ct.ProcessType {
	return ct.ProcessType{Resources: resource.Resources{
		resource.TypeMemory: {Limit: mibPtr(1)},
	}}
}

type formationStop struct {
	appID, releaseID string
	types            []string
}

// zeroFormations drops process counts so FormationListActive, which is
// cluster-wide, does not see formations left by these tests.
func (s *S) zeroFormations(c *C, rows []formationStop) {
	for _, row := range rows {
		if row.appID == "" || row.releaseID == "" {
			continue
		}
		procs := make(map[string]int, len(row.types))
		for _, name := range row.types {
			procs[name] = 0
		}
		c.Assert(s.c.PutFormation(&ct.Formation{
			AppID: row.appID, ReleaseID: row.releaseID, Processes: procs,
		}), IsNil)
	}
}

func (s *S) TestAccountProcessQuota(c *C) {
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	var cleanup []formationStop
	defer func() {
		s.zeroFormations(c, cleanup)
		c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	}()

	mem := 100
	quota := 20
	app := s.createTestApp(c, &ct.App{Name: "q4-hosted-quota", OwnerAccount: "user:q4-hosted-quota"})
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: app.OwnerAccount, MaxProcesses: &quota, MaxMemoryMB: &mem,
	}), IsNil)
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess(), "worker": oneMiBProcess(), "slugbuilder": oneMiBProcess()},
	})
	// Usage counts the current release only. Scale the release deploy would mark current.
	c.Assert(s.api.appRepo.SetRelease(app, release.ID), IsNil)
	cleanup = append(cleanup, formationStop{app.ID, release.ID, []string{"web", "worker", "slugbuilder"}})
	put := func(procs map[string]int) error {
		return s.c.PutFormation(&ct.Formation{
			AppID: app.ID, ReleaseID: release.ID, Processes: procs,
		})
	}

	// Hosted owned apps follow the account quota, not 10 per type.
	c.Assert(put(map[string]int{"web": 11}), IsNil)
	err := put(map[string]int{"web": 21})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)
	c.Assert(strings.Contains(err.Error(), "cluster maximum"), Equals, false)

	// One request that names both types is over the account sum.
	tight := 15
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: app.OwnerAccount, MaxProcesses: &tight, MaxMemoryMB: &mem,
	}), IsNil)
	c.Assert(put(map[string]int{"web": 0, "worker": 0}), IsNil)
	err = put(map[string]int{"web": 10, "worker": 10})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// CLI scale sends only the named type. The stored worker must still count.
	c.Assert(put(map[string]int{"web": 10, "worker": 0}), IsNil)
	err = put(map[string]int{"worker": 10})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	g := &grpcAPI{controllerAPI: s.api}
	err = g.enforceCreateScale(app.ID, release.ID, map[string]int{"worker": 10}, nil, false)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// Under the process quota and over memory.
	lowMem := 10
	wide := 20
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: app.OwnerAccount, MaxProcesses: &wide, MaxMemoryMB: &lowMem,
	}), IsNil)
	err = put(map[string]int{"web": 11, "worker": 0})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)
	c.Assert(strings.Contains(err.Error(), "cluster maximum"), Equals, false)

	// Another app already holds the rest of the account.
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: app.OwnerAccount, MaxProcesses: &tight, MaxMemoryMB: &mem,
	}), IsNil)
	c.Assert(put(map[string]int{"web": 10, "worker": 0}), IsNil)
	other := s.createTestApp(c, &ct.App{Name: "q4-hosted-other", OwnerAccount: app.OwnerAccount})
	otherRel := s.createTestRelease(c, other.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess()},
	})
	c.Assert(s.api.appRepo.SetRelease(other, otherRel.ID), IsNil)
	cleanup = append(cleanup, formationStop{other.ID, otherRel.ID, []string{"web"}})
	err = s.c.PutFormation(&ct.Formation{
		AppID: other.ID, ReleaseID: otherRel.ID, Processes: map[string]int{"web": 6},
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// No quota row stays on the hosted free tier: 4 processes, 512 MB.
	free := s.createTestApp(c, &ct.App{Name: "q4-hosted-free", OwnerAccount: "user:q4-hosted-free"})
	freeRel := s.createTestRelease(c, free.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess()},
	})
	c.Assert(s.api.appRepo.SetRelease(free, freeRel.ID), IsNil)
	cleanup = append(cleanup, formationStop{free.ID, freeRel.ID, []string{"web"}})
	freePut := func(n int) error {
		return s.c.PutFormation(&ct.Formation{
			AppID: free.ID, ReleaseID: freeRel.ID, Processes: map[string]int{"web": n},
		})
	}
	c.Assert(freePut(4), IsNil)
	err = freePut(5)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// A different account at quota 4 cannot scale to 5.
	four := 4
	small := s.createTestApp(c, &ct.App{Name: "q4-hosted-four", OwnerAccount: "user:q4-hosted-four"})
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: small.OwnerAccount, MaxProcesses: &four, MaxMemoryMB: &mem,
	}), IsNil)
	smallRel := s.createTestRelease(c, small.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess()},
	})
	c.Assert(s.api.appRepo.SetRelease(small, smallRel.ID), IsNil)
	cleanup = append(cleanup, formationStop{small.ID, smallRel.ID, []string{"web"}})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: small.ID, ReleaseID: smallRel.ID, Processes: map[string]int{"web": 4},
	}), IsNil)
	err = s.c.PutFormation(&ct.Formation{
		AppID: small.ID, ReleaseID: smallRel.ID, Processes: map[string]int{"web": 5},
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// Unlimited process count still fails projected memory.
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: app.OwnerAccount, MaxMemoryMB: &lowMem,
	}), IsNil)
	err = put(map[string]int{"web": 11, "worker": 0})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// Internal types stay outside the cluster cap. Empty owner keeps the cap.
	unowned := s.createTestApp(c, &ct.App{Name: "q4-hosted-unowned"})
	unownedRel := s.createTestRelease(c, unowned.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess(), "slugbuilder": oneMiBProcess()},
	})
	cleanup = append(cleanup, formationStop{unowned.ID, unownedRel.ID, []string{"web", "slugbuilder"}})
	err = s.c.PutFormation(&ct.Formation{
		AppID: unowned.ID, ReleaseID: unownedRel.ID,
		Processes: map[string]int{"web": 11, "slugbuilder": 40},
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "cluster maximum"), Equals, true)
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: unowned.ID, ReleaseID: unownedRel.ID,
		Processes: map[string]int{"web": 10, "slugbuilder": 40},
	}), IsNil)

	// Self-hosted keeps the per-type cap even when the account quota is higher.
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	self := s.createTestApp(c, &ct.App{Name: "q4-self-quota", OwnerAccount: "user:q4-self-quota"})
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: self.OwnerAccount, MaxProcesses: &quota, MaxMemoryMB: &mem,
	}), IsNil)
	selfRel := s.createTestRelease(c, self.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess()},
	})
	c.Assert(s.api.appRepo.SetRelease(self, selfRel.ID), IsNil)
	cleanup = append(cleanup, formationStop{self.ID, selfRel.ID, []string{"web"}})
	err = s.c.PutFormation(&ct.Formation{
		AppID: self.ID, ReleaseID: selfRel.ID, Processes: map[string]int{"web": 11},
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "cluster maximum"), Equals, true)
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: self.ID, ReleaseID: selfRel.ID, Processes: map[string]int{"web": 10},
	}), IsNil)

	err = put(map[string]int{"release": 1})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "cannot be scaled"), Equals, true)
}

func (s *S) putRuntimeSettingsRaw(c *C, body string) *http.Response {
	req, err := http.NewRequest(http.MethodPut, s.srv.URL+"/cluster/runtime-settings", strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	return res
}

func (s *S) TestAccountProcessCapUnlimited(c *C) {
	c.Assert(s.hc.db.Exec("UPDATE runtime_settings SET max_processes = NULL WHERE id = 1"), IsNil)
	var cleanup []formationStop
	defer func() {
		s.zeroFormations(c, cleanup)
		c.Assert(s.hc.db.Exec("UPDATE runtime_settings SET max_processes = 10, allow_custom_limits = false WHERE id = 1"), IsNil)
	}()

	cur, err := s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	_, unlimited := cur.ProcessCap()
	c.Assert(unlimited, Equals, true)

	// A client that loaded NULL and writes another field must not restore 10.
	next := cur.Copy()
	next.AllowCustomLimits = true
	c.Assert(s.c.UpdateRuntimeSettings(&next), IsNil)
	got, err := s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	_, unlimited = got.ProcessCap()
	c.Assert(unlimited, Equals, true)

	// An omitted field keeps NULL. 0 is not the sentinel.
	res := s.putRuntimeSettingsRaw(c, `{"allow_custom_limits":true,"reserve_resources":false}`)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 200)
	got, err = s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	_, unlimited = got.ProcessCap()
	c.Assert(unlimited, Equals, true)

	res = s.putRuntimeSettingsRaw(c, `{"allow_custom_limits":true,"max_processes":0}`)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	c.Assert(res.StatusCode, Equals, 400)
	c.Assert(strings.Contains(string(body), "max_processes"), Equals, true)
	got, err = s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	_, unlimited = got.ProcessCap()
	c.Assert(unlimited, Equals, true)

	mem := 100
	app := s.createTestApp(c, &ct.App{Name: "q4-self-unlimited", OwnerAccount: "user:q4-self-unlimited"})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess(), "worker": oneMiBProcess()},
	})
	c.Assert(s.api.appRepo.SetRelease(app, release.ID), IsNil)
	cleanup = append(cleanup, formationStop{app.ID, release.ID, []string{"web", "worker"}})
	// No quota row: self-hosted count is unlimited once the per-type cap is cleared.
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 11},
	}), IsNil)

	// A quota row still applies after the cap is cleared.
	quota := 12
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{
		Account: app.OwnerAccount, MaxProcesses: &quota, MaxMemoryMB: &mem,
	}), IsNil)
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 11, "worker": 1},
	}), IsNil)
	err = s.c.PutFormation(&ct.Formation{
		AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 11, "worker": 2},
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "quota"), Equals, true)

	// Empty owner keeps a cleared cap as unlimited too: there is no account quota.
	// The host cap is a different limit. 11 must be accepted for an unowned app
	// only when the cluster cap is unlimited. Hosted mode is not required.
	unowned := s.createTestApp(c, &ct.App{Name: "q4-self-unlimited-platform"})
	unownedRel := s.createTestRelease(c, unowned.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": oneMiBProcess()},
	})
	c.Assert(s.api.appRepo.SetRelease(unowned, unownedRel.ID), IsNil)
	cleanup = append(cleanup, formationStop{unowned.ID, unownedRel.ID, []string{"web"}})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: unowned.ID, ReleaseID: unownedRel.ID, Processes: map[string]int{"web": 11},
	}), IsNil)
}

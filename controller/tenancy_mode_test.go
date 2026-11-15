package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

func (s *S) putTenancyRaw(c *C, body string) (int, hh.JSONError, ct.TenancySettings) {
	req, err := http.NewRequest(http.MethodPut, s.srv.URL+"/tenancy", strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	var apiErr hh.JSONError
	var settings ct.TenancySettings
	if res.StatusCode >= 400 {
		c.Assert(json.Unmarshal(raw, &apiErr), IsNil)
		return res.StatusCode, apiErr, settings
	}
	c.Assert(json.Unmarshal(raw, &settings), IsNil)
	return res.StatusCode, apiErr, settings
}

func (s *S) hideNonSystemOwnedApps(c *C) []string {
	rows, err := s.hc.db.Query(`
		UPDATE apps SET deleted_at = now()
		WHERE deleted_at IS NULL
		  AND COALESCE(owner_account, '') <> ''
		  AND COALESCE(meta->>'flynn-system-app', '') <> 'true'
		RETURNING app_id::text`)
	c.Assert(err, IsNil)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		c.Assert(rows.Scan(&id), IsNil)
		ids = append(ids, id)
	}
	c.Assert(rows.Err(), IsNil)
	return ids
}

func (s *S) restoreHiddenApps(c *C, ids []string) {
	for _, id := range ids {
		c.Assert(s.hc.db.Exec(`UPDATE apps SET deleted_at = NULL WHERE app_id = $1`, id), IsNil)
	}
}

func (s *S) restoreTenancyDefaults(c *C) {
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE cluster_settings SET signup_enabled = false WHERE id = 1`), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE runtime_settings SET reserve_resources = false WHERE id = 1`), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE runtime_profiles SET reserve_resources = false WHERE name = 'small'`), IsNil)
}

// TestPutTenancyIgnoresSignupEnabled: the mode PUT writes tenancy_mode only.
// A signup_enabled field must not turn the column on or clear it.
func (s *S) TestPutTenancyIgnoresSignupEnabled(c *C) {
	defer s.restoreTenancyDefaults(c)
	c.Assert(s.hc.db.Exec(`UPDATE cluster_settings SET tenancy_mode = 'self_hosted', signup_enabled = true WHERE id = 1`), IsNil)

	code, _, settings := s.putTenancyRaw(c, `{"mode":"hosted","signup_enabled":false}`)
	c.Assert(code, Equals, 200)
	c.Assert(settings.Mode, Equals, tenancy.ModeHosted)
	c.Assert(settings.SignupEnabled, Equals, true)

	stored, err := s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(stored.Mode, Equals, tenancy.ModeHosted)
	c.Assert(stored.SignupEnabled, Equals, true)
}

// TestPutTenancyRefusesSelfHostedReversal: switching hosted → self_hosted while
// a non-system app has an owner is not a safe reversal. Quota rows and
// reserve_resources stay. Jobs are not rewritten. An already self-hosted
// cluster is not blocked. When the reversal is allowed, a missing quota row
// is unlimited immediately and a stored row still applies.
func (s *S) TestPutTenancyRefusesSelfHostedReversal(c *C) {
	defer s.restoreTenancyDefaults(c)
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE runtime_settings SET reserve_resources = true WHERE id = 1`), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE runtime_profiles SET reserve_resources = true WHERE name = 'small'`), IsNil)

	kept := 3
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: "user:f1-kept", MaxApps: &kept}), IsNil)

	suffix := strings.ReplaceAll(random.UUID(), "-", "")[:10]
	unowned := s.createTestApp(c, &ct.App{Name: "f1-free-" + suffix})
	release := s.createTestRelease(c, unowned.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID: unowned.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 2},
	}), IsNil)
	before, err := s.c.GetFormation(unowned.ID, release.ID)
	c.Assert(err, IsNil)

	customer := s.createTestApp(c, &ct.App{
		Name:         "f1-cust-" + suffix,
		OwnerAccount: "user:f1-customer",
	})
	_ = customer

	code, apiErr, _ := s.putTenancyRaw(c, `{"mode":"self_hosted"}`)
	c.Assert(code, Equals, 400)
	c.Assert(apiErr.Code, Equals, hh.ValidationErrorCode)
	c.Assert(apiErr.Message, Matches, ".*owner_account.*")
	c.Assert(apiErr.Message, Matches, ".*not a safe reversal.*")

	stored, err := s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(stored.Mode, Equals, tenancy.ModeHosted)
	c.Assert(stored.SignupEnabled, Equals, false)

	quota, err := s.api.tenancy.GetQuota("user:f1-kept")
	c.Assert(err, IsNil)
	c.Assert(quota.MaxApps, NotNil)
	c.Assert(*quota.MaxApps, Equals, 3)

	settings, err := s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	c.Assert(settings.ReserveResources, Equals, true)
	profiles, err := s.c.ListRuntimeProfiles()
	c.Assert(err, IsNil)
	var small bool
	for _, p := range profiles {
		if p != nil && p.Name == "small" {
			small = p.ReserveResources
		}
	}
	c.Assert(small, Equals, true)

	after, err := s.c.GetFormation(unowned.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(after.Processes["web"], Equals, 2)
	c.Assert(after.UpdatedAt, NotNil)
	c.Assert(before.UpdatedAt, NotNil)
	c.Assert(after.UpdatedAt.Equal(*before.UpdatedAt), Equals, true)

	// A system app with an owner does not count. A deleted customer app does not
	// count. Earlier tests in this process leave their own owned apps, so hide
	// every non-system owned app for the allow path and put them back after.
	hidden := s.hideNonSystemOwnedApps(c)
	defer s.restoreHiddenApps(c, hidden)
	s.createTestApp(c, &ct.App{
		Name:         "f1-sys-" + suffix,
		OwnerAccount: "user:f1-system",
		Meta:         map[string]string{"flynn-system-app": "true"},
	})
	code, _, settingsOut := s.putTenancyRaw(c, `{"mode":"self_hosted"}`)
	c.Assert(code, Equals, 200)
	c.Assert(settingsOut.Mode, Equals, tenancy.ModeSelfHosted)
	c.Assert(settingsOut.SignupEnabled, Equals, false)

	quota, err = s.api.tenancy.GetQuota("user:f1-kept")
	c.Assert(err, IsNil)
	c.Assert(*quota.MaxApps, Equals, 3)
	if err := s.api.quotaAllows("user:f1-kept", 4, 0, 0, 0, 0); err == nil {
		c.Fatal("stored quota row must still apply in self_hosted")
	}
	if err := s.api.quotaAllows("user:f1-nobody", 1000, 0, 0, 0, 0); err != nil {
		c.Fatal("no quota row is unlimited immediately in self_hosted, including while apps still run")
	}
	after, err = s.c.GetFormation(unowned.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(after.Processes["web"], Equals, 2)
	settings, err = s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	c.Assert(settings.ReserveResources, Equals, true)

	// Already self-hosted: a customer app does not block a repeat self_hosted PUT.
	s.createTestApp(c, &ct.App{
		Name:         "f1-again-" + suffix,
		OwnerAccount: "user:f1-again",
	})
	code, _, settingsOut = s.putTenancyRaw(c, `{"mode":"self_hosted"}`)
	c.Assert(code, Equals, 200)
	c.Assert(settingsOut.Mode, Equals, tenancy.ModeSelfHosted)
}

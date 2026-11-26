package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/tenancy"
	tu "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/productioncheck"
)

// withHostedCheck runs the PUT /tenancy hosted guard as check for the test.
func (s *S) withHostedCheck(c *C, check func() error) func() {
	prev := s.api.hostedCheck
	s.api.hostedCheck = check
	return func() { s.api.hostedCheck = prev }
}

// TestPutTenancyHostedRunsProductionCheck: NEW-45. PUT /tenancy hosted runs
// the production:check rows on the controller, so a direct PUT (dashboard,
// curl, flynn CLI) cannot skip them. A failing row refuses the switch and
// names the rows. The cluster key can override with force, which is
// recorded as tenancy.force_hosted before the mode changes. A user may not
// force. A passing check switches with no force row, and a cluster that is
// already hosted is not checked again.
func (s *S) TestPutTenancyHostedRunsProductionCheck(c *C) {
	defer s.restoreTenancyDefaults(c)
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	runs := 0
	failing := func() error {
		runs++
		return &productioncheck.HostedBlockError{Fails: []string{"blobstore", "log-sink"}}
	}
	defer s.withHostedCheck(c, failing)()

	var start int64
	c.Assert(s.hc.db.QueryRow(`SELECT COALESCE(max(id), 0) FROM core_audit`).Scan(&start), IsNil)

	code, apiErr, _ := s.putTenancyRaw(c, `{"mode":"hosted"}`)
	c.Assert(code, Equals, 400)
	c.Assert(apiErr.Code, Equals, hh.ValidationErrorCode)
	c.Assert(apiErr.Message, Matches, ".*production:check would fail \\(blobstore, log-sink\\).*")
	c.Assert(apiErr.Message, Matches, ".*force.*")
	c.Assert(runs, Equals, 1)
	stored, err := s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(stored.Mode, Equals, tenancy.ModeSelfHosted)

	// A cluster admin user may PUT /tenancy but may not force.
	rec := s.putTenancyWithToken(c, &authorizer.Token{UserID: "u-admin", Scopes: []string{"cluster:admin"}}, `{"mode":"hosted","force":true}`)
	c.Assert(rec.Code, Equals, 403, Commentf("body %s", rec.Body.String()))
	c.Assert(rec.Body.String(), Matches, "(?s).*cluster key.*")
	// And without force the same failing check refuses it.
	rec = s.putTenancyWithToken(c, &authorizer.Token{UserID: "u-admin", Scopes: []string{"cluster:admin"}}, `{"mode":"hosted"}`)
	c.Assert(rec.Code, Equals, 400, Commentf("body %s", rec.Body.String()))
	c.Assert(s.auditActionsSince(c, start, "tenancy.force_hosted"), HasLen, 0)

	// The cluster key forces past the failing rows, with an audit row.
	code, _, settings := s.putTenancyRaw(c, `{"mode":"hosted","force":true}`)
	c.Assert(code, Equals, 200)
	c.Assert(settings.Mode, Equals, tenancy.ModeHosted)
	forced := s.auditActionsSince(c, start, "tenancy.force_hosted")
	c.Assert(forced, HasLen, 1)
	c.Assert(forced[0].Actor, Equals, "cluster-key")
	var data struct {
		From    string   `json:"from"`
		Mode    string   `json:"mode"`
		Force   bool     `json:"force"`
		Failing []string `json:"failing"`
		Error   string   `json:"error"`
		Warning string   `json:"warning"`
	}
	c.Assert(json.Unmarshal(forced[0].Data, &data), IsNil)
	c.Assert(data.From, Equals, tenancy.ModeSelfHosted)
	c.Assert(data.Mode, Equals, tenancy.ModeHosted)
	c.Assert(data.Force, Equals, true)
	c.Assert(data.Failing, DeepEquals, []string{"blobstore", "log-sink"})
	c.Assert(data.Warning, Matches, ".*production:check.*")

	// Already hosted: the switch is a plain set and the check is not run.
	runs = 0
	code, _, _ = s.putTenancyRaw(c, `{"mode":"hosted"}`)
	c.Assert(code, Equals, 200)
	c.Assert(runs, Equals, 0)

	// A passing check switches without force and without a force row.
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	s.api.hostedCheck = func() error { return nil }
	code, _, settings = s.putTenancyRaw(c, `{"mode":"hosted"}`)
	c.Assert(code, Equals, 200)
	c.Assert(settings.Mode, Equals, tenancy.ModeHosted)
	c.Assert(s.auditActionsSince(c, start, "tenancy.force_hosted"), HasLen, 1)

	// A check that cannot run refuses too: the switch is not unguarded.
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil)
	s.api.hostedCheck = func() error { return errors.New("controller.discoverd: no route") }
	code, apiErr, _ = s.putTenancyRaw(c, `{"mode":"hosted"}`)
	c.Assert(code, Equals, 400)
	c.Assert(apiErr.Message, Matches, ".*production:check.*no route.*force.*")
	stored, err = s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(stored.Mode, Equals, tenancy.ModeSelfHosted)
}

// hostedCheckAPI answers the controller reads of production:check from a
// path map; a missing path is an error, so its row fails.
type hostedCheckAPI struct {
	paths   map[string]interface{}
	release *ct.Release
}

func (a hostedCheckAPI) Get(path string, out interface{}) error {
	v, ok := a.paths[path]
	if !ok {
		return errors.New("unexpected GET " + path)
	}
	raw, _ := json.Marshal(v)
	return json.Unmarshal(raw, out)
}

func (a hostedCheckAPI) GetAppRelease(appID string) (*ct.Release, error) {
	if appID == "blobstore" && a.release != nil {
		return a.release, nil
	}
	return nil, ct.ErrNotFound
}

func (a hostedCheckAPI) ListRuntimeProfiles() ([]*ct.RuntimeProfile, error) { return nil, nil }

// TestHostedCheckConfigRunsTheServerRows: the controller's guard runs the
// production:check rows that it can read itself (API, discoverd hosts, host
// jobs) and never the two rows that read the CLI's own host. A postgres
// blobstore fails when judged as hosted, and the hosts come from the
// cluster client.
func TestHostedCheckConfigRunsTheServerRows(t *testing.T) {
	hc := tu.NewFakeHostClient("h1", false)
	api := &controllerAPI{clusterClient: tu.NewFakeClusterWithHosts(map[string]utils.HostClient{"h1": hc})}
	check := hostedCheckAPI{
		paths: map[string]interface{}{
			"/tenancy":           ct.TenancySettings{Mode: "self_hosted"},
			"/cluster/placement": ct.WorkloadPlacement{Mode: "mixed"},
		},
		release: &ct.Release{Env: map[string]string{"DEFAULT_BACKEND": "postgres", "AUTH_KEY": "k", "ACCESS_TOKEN_KEY": "t"}},
	}
	cfg := api.hostedCheckConfig(check)
	if cfg.ZFS != nil || cfg.Overlay != nil {
		t.Fatal("the controller must not run the host-local zfs and overlay rows")
	}
	rows := productioncheck.Collect(cfg)
	seen := map[string]string{}
	for _, r := range rows {
		seen[r.Check] = r.Status
	}
	for _, local := range []string{"zfs", "overlay"} {
		if _, ok := seen[local]; ok {
			t.Fatalf("row %s must not run on the controller: %+v", local, rows)
		}
	}
	if seen["host-cap"] != "fail" {
		t.Fatalf("host-cap reads the cluster's hosts (h1 has no flynn-max-processes): %+v", rows)
	}
	err := productioncheck.HostedBlock(cfg)
	var block *productioncheck.HostedBlockError
	if !errors.As(err, &block) {
		t.Fatalf("a postgres blobstore must block hosted: %v", err)
	}
	if !strings.Contains(err.Error(), "blobstore") {
		t.Fatalf("error %q does not name the blobstore row", err)
	}
}

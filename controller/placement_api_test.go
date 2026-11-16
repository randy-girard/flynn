package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/placement"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func (s *S) putPlacementRaw(c *C, body string) (int, hh.JSONError, ct.WorkloadPlacement) {
	req, err := http.NewRequest(http.MethodPut, s.srv.URL+"/cluster/placement", strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	var apiErr hh.JSONError
	var settings ct.WorkloadPlacement
	if res.StatusCode >= 400 {
		c.Assert(json.Unmarshal(raw, &apiErr), IsNil)
		return res.StatusCode, apiErr, settings
	}
	c.Assert(json.Unmarshal(raw, &settings), IsNil)
	return res.StatusCode, apiErr, settings
}

func (s *S) restorePlacement(c *C) {
	c.Assert(s.api.tenancy.SetWorkloadPlacement(placement.ModeMixed), IsNil)
	s.cc.SetHosts(map[string]utils.HostClient{})
}

func (s *S) TestGetPlacementDefaultsToMixed(c *C) {
	defer s.restorePlacement(c)
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+"/cluster/placement", nil)
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 200)
	var got ct.WorkloadPlacement
	c.Assert(json.NewDecoder(res.Body).Decode(&got), IsNil)
	c.Assert(got.Mode, Equals, placement.ModeMixed)
}

func (s *S) TestPutSplitRefusesWithoutCoreHost(c *C) {
	defer s.restorePlacement(c)
	app := testutils.NewFakeHostClient("app-1", false)
	c.Assert(app.UpdateTags(map[string]string{placement.TagRole: placement.RoleApp}), IsNil)
	down := testutils.NewFakeHostClient("core-down", false)
	c.Assert(down.UpdateTags(map[string]string{placement.TagRole: placement.RoleCore}), IsNil)
	down.Shutdown = true
	s.cc.SetHosts(map[string]utils.HostClient{"app-1": app, "core-down": down})

	code, apiErr, _ := s.putPlacementRaw(c, `{"mode":"split"}`)
	c.Assert(code, Equals, 400)
	c.Assert(strings.Contains(apiErr.Message, "flynn-role=core"), Equals, true)
	mode, err := s.api.tenancy.GetWorkloadPlacement()
	c.Assert(err, IsNil)
	c.Assert(mode, Equals, placement.ModeMixed)
}

func (s *S) TestPutSplitAllowsZeroAppHosts(c *C) {
	defer s.restorePlacement(c)
	core := testutils.NewFakeHostClient("core-1", false)
	c.Assert(core.UpdateTags(map[string]string{placement.TagRole: placement.RoleCore}), IsNil)
	s.cc.SetHosts(map[string]utils.HostClient{"core-1": core})

	code, _, got := s.putPlacementRaw(c, `{"mode":"split"}`)
	c.Assert(code, Equals, 200)
	c.Assert(got.Mode, Equals, placement.ModeSplit)

	code, _, got = s.putPlacementRaw(c, `{"mode":"mixed"}`)
	c.Assert(code, Equals, 200)
	c.Assert(got.Mode, Equals, placement.ModeMixed)
}

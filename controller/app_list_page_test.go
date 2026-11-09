package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) signUser(c *C, userID string, grants []*api.AppGrant) string {
	now := time.Now()
	tok, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     userID,
		UserEmail:  userID + "@flynn.test",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		AppGrants:  grants,
	})
	c.Assert(err, IsNil)
	return tok
}

func (s *S) getApps(c *C, auth string, basic bool, rawQuery string) (int, []*ct.App) {
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+"/apps?"+rawQuery, nil)
	c.Assert(err, IsNil)
	if basic {
		req.SetBasicAuth("", auth)
	} else {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil
	}
	var apps []*ct.App
	c.Assert(json.Unmarshal(body, &apps), IsNil)
	return res.StatusCode, apps
}

func (s *S) setCreatedAt(c *C, id string, at time.Time) {
	c.Assert(s.hc.db.Exec(`UPDATE apps SET created_at = $1 WHERE app_id = $2::uuid`, at, id), IsNil)
}

// TestAppListVisiblePage is the production list: visibility before LIMIT, for a
// user token and for a cluster-admin catalog. Tenancy mode is not involved.
func (s *S) TestAppListVisiblePage(c *C) {
	const (
		userID  = "c4-page-user"
		otherID = "c4-page-other"
	)
	stamp := time.Date(2099, 6, 1, 12, 0, 0, 123456000, time.UTC)
	ownHigh := s.createTestApp(c, &ct.App{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000003", Name: "c4-own-high", OwnerAccount: "user:" + userID,
	})
	ownLow := s.createTestApp(c, &ct.App{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000001", Name: "c4-own-low", OwnerAccount: "user:" + userID,
	})
	shared := s.createTestApp(c, &ct.App{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000002", Name: "c4-shared", OwnerAccount: "user:" + otherID,
	})
	byName := s.createTestApp(c, &ct.App{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000004", Name: "c4-by-name", OwnerAccount: "user:" + otherID,
	})
	hidden := s.createTestApp(c, &ct.App{
		ID: "ffffffff-ffff-4fff-bfff-000000000001", Name: "c4-hidden", OwnerAccount: "user:" + otherID,
	})
	emptyGrant := s.createTestApp(c, &ct.App{
		ID: "ffffffff-ffff-4fff-bfff-000000000003", Name: "c4-empty-grant", OwnerAccount: "user:" + otherID,
	})
	systemApp := s.createTestApp(c, &ct.App{
		ID: "ffffffff-ffff-4fff-bfff-000000000002", Name: "c4-system", OwnerAccount: "user:" + userID,
		Meta: map[string]string{"flynn-system-app": "true"},
	})
	platform := s.createTestApp(c, &ct.App{
		ID: "ffffffff-ffff-4fff-bfff-000000000004", Name: "tarreceive", OwnerAccount: "user:" + userID,
		Meta: map[string]string{"flynn-system-app": "true"},
	})
	plugin := s.createTestApp(c, &ct.App{
		ID: "ffffffff-ffff-4fff-8fff-000000000010", Name: "c4-plugin",
		Meta: map[string]string{"flynn-system-app": "true", "flynn-plugin": "true"},
	})
	// Platform name with no system meta. Reserved at create time, so rename
	// after insert. A user who owns it still must not see it.
	statusApp := s.createTestApp(c, &ct.App{
		ID: "ffffffff-ffff-4fff-bfff-000000000005", Name: "c4-status-holder", OwnerAccount: "user:" + userID,
	})
	c.Assert(s.hc.db.Exec(`UPDATE apps SET name = 'status' WHERE app_id = $1::uuid`, statusApp.ID), IsNil)
	for _, app := range []*ct.App{ownHigh, ownLow, shared, byName, hidden, emptyGrant, systemApp, platform, plugin, statusApp} {
		s.setCreatedAt(c, app.ID, stamp)
	}

	tok := s.signUser(c, userID, []*api.AppGrant{
		{AppId: shared.ID, Permissions: []string{"app:read"}},
		{AppId: byName.Name, Permissions: []string{"app:deploy"}},
		{AppId: emptyGrant.ID, Permissions: nil},
		{AppId: systemApp.ID, Permissions: []string{"app:read"}},
	})

	code, _ := s.getApps(c, tok, false, "count=0")
	c.Assert(code, Equals, http.StatusBadRequest)

	code, unpaged := s.getApps(c, tok, false, "page=2")
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(appIDs(unpaged), DeepEquals, []string{ownLow.ID, shared.ID, ownHigh.ID, byName.ID})

	code, first := s.getApps(c, tok, false, "count=1&all=true&system=true")
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(first, HasLen, 1)
	c.Assert(first[0].ID, Equals, byName.ID)
	c.Assert(first[0].OwnerAccount, Equals, "user:"+otherID)

	cursor := url.Values{}
	cursor.Set("count", "1")
	cursor.Set("before", first[0].CreatedAt.UTC().Format(time.RFC3339Nano))
	cursor.Set("before_id", first[0].ID)
	code, second := s.getApps(c, tok, false, cursor.Encode())
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(second, HasLen, 1)
	c.Assert(second[0].ID, Equals, ownHigh.ID)

	cursor.Set("before", second[0].CreatedAt.UTC().Format(time.RFC3339Nano))
	cursor.Set("before_id", second[0].ID)
	code, third := s.getApps(c, tok, false, cursor.Encode())
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(third, HasLen, 1)
	c.Assert(third[0].ID, Equals, shared.ID)

	cursor.Set("before", third[0].CreatedAt.UTC().Format(time.RFC3339Nano))
	cursor.Set("before_id", third[0].ID)
	code, fourth := s.getApps(c, tok, false, cursor.Encode())
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(fourth, HasLen, 1)
	c.Assert(fourth[0].ID, Equals, ownLow.ID)

	cursor.Set("before", fourth[0].CreatedAt.UTC().Format(time.RFC3339Nano))
	cursor.Set("before_id", fourth[0].ID)
	code, done := s.getApps(c, tok, false, cursor.Encode())
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(done, HasLen, 0)

	// before alone skips every row at that timestamp, including the caller's.
	skip := url.Values{}
	skip.Set("count", "10")
	skip.Set("before", first[0].CreatedAt.UTC().Format(time.RFC3339Nano))
	code, skipped := s.getApps(c, tok, false, skip.Encode())
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(skipped, HasLen, 0)

	// Newest rows are a system app and a platform name. The default admin
	// page must still return the plugin, not an empty page and not the system app.
	sysAt := time.Date(2999, 1, 4, 0, 0, 0, 0, time.UTC)
	platformAt := time.Date(2999, 1, 3, 0, 0, 0, 0, time.UTC)
	plugAt := time.Date(2999, 1, 2, 0, 0, 0, 0, time.UTC)
	s.setCreatedAt(c, systemApp.ID, sysAt)
	s.setCreatedAt(c, platform.ID, platformAt)
	s.setCreatedAt(c, plugin.ID, plugAt)

	code, adminPage := s.getApps(c, authKey, true, "count=1")
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(adminPage, HasLen, 1)
	c.Assert(adminPage[0].ID, Equals, plugin.ID)

	code, allPage := s.getApps(c, authKey, true, "all=true&count=1")
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(allPage, HasLen, 1)
	c.Assert(allPage[0].ID, Equals, systemApp.ID)

	scoped, err := controller.NewClientWithToken(s.srv.URL, s.signUser(c, "", []*api.AppGrant{
		{AppId: ownHigh.ID, Permissions: []string{"app:read"}},
	}))
	c.Assert(err, IsNil)
	_, err = scoped.AppListVisible()
	c.Assert(err, NotNil)
}

func appIDs(apps []*ct.App) []string {
	out := make([]string, 0, len(apps))
	for _, app := range apps {
		if app != nil {
			out = append(out, app.ID)
		}
	}
	sort.Strings(out)
	return out
}

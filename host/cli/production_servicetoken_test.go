package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tokenReleaseAPI struct {
	stubProductionAPI
	byApp map[string]*ct.Release
}

func (a *tokenReleaseAPI) GetAppRelease(appID string) (*ct.Release, error) {
	if r, ok := a.byApp[appID]; ok {
		return r, nil
	}
	return nil, errors.New("not found")
}

func signTestServiceToken(t *testing.T, scope string, expires time.Time) string {
	t.Helper()
	_, priv, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tokensigner.ParseSigningKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := signer.Sign(&api.AccessToken{
		UserId:     "billing",
		IssueTime:  timestamppb.New(expires.Add(-24 * time.Hour)),
		ExpireTime: timestamppb.New(expires),
		Scopes:     []string{scope},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func serviceTokenRows(a productionAPI, now time.Time) []productionRow {
	var rows []productionRow
	addServiceTokenLine(func(r productionRow) { rows = append(rows, r) }, a, now)
	return rows
}

func TestProductionCheckWarnsOnExpiringServiceToken(t *testing.T) {
	now := time.Now()
	good := signTestServiceToken(t, "account:quota", now.Add(120*24*time.Hour))
	soon := signTestServiceToken(t, "account:quota", now.Add(10*24*time.Hour))
	gone := signTestServiceToken(t, "acme:issue", now.Add(-time.Hour))

	api := &tokenReleaseAPI{byApp: map[string]*ct.Release{
		"billing-plugin":     {Env: map[string]string{"QUOTA_TOKEN": soon}},
		"letsencrypt-plugin": {Env: map[string]string{"ACME_TOKEN": gone}},
	}}
	rows := serviceTokenRows(api, now)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want a warn for QUOTA_TOKEN and ACME_TOKEN", rows)
	}
	for _, r := range rows {
		if r.status != "warn" || r.check != "service-token" || strings.TrimSpace(r.action) == "" {
			t.Fatalf("row = %+v", r)
		}
		if strings.Contains(r.detail+r.action, soon) || strings.Contains(r.detail+r.action, gone) {
			t.Fatalf("row leaked the token: %+v", r)
		}
	}

	healthy := &tokenReleaseAPI{byApp: map[string]*ct.Release{
		"billing-plugin": {Env: map[string]string{"QUOTA_TOKEN": good}},
	}}
	if rows := serviceTokenRows(healthy, now); len(rows) != 0 {
		t.Fatalf("healthy token or missing plugin must add no row: %+v", rows)
	}
}

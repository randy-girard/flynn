package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"golang.org/x/net/context"
)

func TestCreateReleaseRejectsMissingAppIDForAppToken(t *testing.T) {
	api := &controllerAPI{}
	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{
		AppID:       "app-1",
		Permissions: []string{authz.PermAppEnvWrite},
	}}}
	ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(`{"processes":{"web":{"host_network":true,"args":["web"]}}}`))
	req.Header.Set("Content-Type", "application/json")
	api.CreateRelease(ctx, rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d, body = %s, want 403", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "app_id is required") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestCreateReleaseRejectsMissingAppIDForScaleToken(t *testing.T) {
	api := &controllerAPI{}
	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{
		AppID:       "app-1",
		Permissions: []string{authz.PermAppScaleWrite},
	}}}
	ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	api.CreateRelease(ctx, rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCreateReleaseAdminMissingAppIDNotForbidden(t *testing.T) {
	api := &controllerAPI{}
	tok := &authorizer.Token{ClusterKey: true}
	ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	defer func() {
		_ = recover()
		if rec.Code == 403 {
			t.Fatalf("cluster key without app_id must not be forbidden, body = %s", rec.Body.String())
		}
	}()
	api.CreateRelease(ctx, rec, req)
	if rec.Code == 403 {
		t.Fatalf("cluster key without app_id must not be forbidden, body = %s", rec.Body.String())
	}
}

func TestMayCreateReleaseForApp(t *testing.T) {
	app := &ct.App{ID: "app-1", Name: "demo", Meta: map[string]string{}}
	sys := &ct.App{ID: "sys-1", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}

	envTok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{authz.PermAppEnvWrite}}}}
	scaleTok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{authz.PermAppScaleWrite}}}}
	key := &authorizer.Token{ClusterKey: true}

	if !mayCreateReleaseForApp(envTok, app) {
		t.Fatal("env:write may mint a release")
	}
	if mayCreateReleaseForApp(scaleTok, app) {
		t.Fatal("scale-only token must not mint a release")
	}
	if mayCreateReleaseForApp(envTok, sys) {
		t.Fatal("app-scoped token must not mint a system-app release")
	}
	if !mayCreateReleaseForApp(key, app) || !mayCreateReleaseForApp(key, sys) {
		t.Fatal("cluster key may mint user and system app releases")
	}

	manage, ok := authz.DefaultRoleByID("manage")
	if !ok {
		t.Fatal("missing manage role")
	}
	manageTok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: manage.Permissions}}}
	if !mayCreateReleaseForApp(manageTok, app) {
		t.Fatal("manage must still mint releases")
	}
	deploy, _ := authz.DefaultRoleByID("deploy")
	deployTok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: deploy.Permissions}}}
	if mayCreateReleaseForApp(deployTok, app) {
		t.Fatal("deploy ships an existing release; it must not mint one")
	}
}

func TestReleaseBelongsToApp(t *testing.T) {
	app := &ct.App{ID: "app-1"}
	if !releaseBelongsToApp(&ct.Release{AppID: "app-1"}, app) {
		t.Fatal("same-app release must attach")
	}
	if releaseBelongsToApp(&ct.Release{AppID: "other"}, app) {
		t.Fatal("cross-app attach must be rejected")
	}
	if !releaseBelongsToApp(&ct.Release{AppID: ""}, app) {
		t.Fatal("unscoped (legacy/admin) release may attach")
	}
	if releaseBelongsToApp(nil, app) || releaseBelongsToApp(&ct.Release{AppID: "app-1"}, nil) {
		t.Fatal("nil release or app must not attach")
	}
}

func TestSanitizeReleaseProcesses(t *testing.T) {
	user := &ct.App{ID: "app-1", Meta: map[string]string{}}
	priv := ct.ProcessType{
		HostNetwork:       true,
		HostPIDNamespace:  true,
		WriteableCgroups:  true,
		LinuxCapabilities: []string{"CAP_SYS_ADMIN"},
		Mounts:            []host.Mount{{Target: "/var/run/docker.sock"}},
		Profiles:          []host.JobProfile{host.JobProfileZFS},
		Args:              []string{"web"},
	}
	rel := &ct.Release{Processes: map[string]ct.ProcessType{"web": priv}}
	if !sanitizeReleaseProcesses(user, rel) {
		t.Fatal("user app with privileged fields must report persist")
	}
	got := rel.Processes["web"]
	if got.HostNetwork || got.HostPIDNamespace || got.WriteableCgroups ||
		len(got.LinuxCapabilities) != 0 || len(got.Mounts) != 0 || len(got.Profiles) != 0 {
		t.Fatalf("privileged fields survived: %+v", got)
	}
	if len(got.Args) != 1 || got.Args[0] != "web" {
		t.Fatalf("args = %v", got.Args)
	}

	sys := &ct.App{ID: "sys-1", Meta: map[string]string{"flynn-system-app": "true"}}
	sysRel := &ct.Release{Processes: map[string]ct.ProcessType{"web": priv}}
	if sanitizeReleaseProcesses(sys, sysRel) {
		t.Fatal("system app must not strip privileged fields")
	}
	if !sysRel.Processes["web"].HostNetwork || len(sysRel.Processes["web"].LinuxCapabilities) != 1 {
		t.Fatalf("system app privileged fields changed: %+v", sysRel.Processes["web"])
	}

	clean := &ct.Release{Processes: map[string]ct.ProcessType{"web": {Args: []string{"web"}}}}
	if sanitizeReleaseProcesses(user, clean) {
		t.Fatal("already-clean user release must not need persist")
	}
}

func TestSetAppReleaseRejectsCrossApp(t *testing.T) {
	app := &ct.App{ID: "app-1", Meta: map[string]string{}}
	release := &ct.Release{ID: "rel-other", AppID: "other"}
	if releaseBelongsToApp(release, app) {
		t.Fatal("cross-app attach must be rejected")
	}
}

func TestProcessTypesPrivilegedJSON(t *testing.T) {
	in := map[string]ct.ProcessType{"web": {HostNetwork: true, Args: []string{"web"}}}
	if !processTypesPrivileged(in) {
		t.Fatal("host_network must count as privileged")
	}
	b, err := json.Marshal(stripPrivilegedProcessTypes(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "host_network") {
		t.Fatalf("stripped JSON leaked host_network: %s", b)
	}
}

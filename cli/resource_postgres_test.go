package main

import (
	"os"
	"strings"
	"testing"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/pgappliance"
)

func TestCreatedResourceMessage(t *testing.T) {
	res := &ct.Resource{
		ID:         "919a764d-863d-4629-aa23-76248728dcbc",
		ExternalID: "deadbeefdeadbeef",
		Env:        map[string]string{"FLYNN_MYSQL": "mysql-harbor-kxmnpq"},
	}
	got := createdResourceMessage(res, "")
	if got != "Created add-on mysql-harbor-kxmnpq and a new release." {
		t.Fatalf("name: %q", got)
	}
	got = createdResourceMessage(res, "analytics")
	if got != "Created add-on mysql-harbor-kxmnpq (as ANALYTICS) and a new release." {
		t.Fatalf("as: %q", got)
	}
	got = createdResourceMessage(&ct.Resource{ID: res.ID, ExternalID: res.ExternalID}, "CACHE")
	if got != "Created add-on CACHE and a new release." {
		t.Fatalf("as without env: %q", got)
	}
	got = createdResourceMessage(&ct.Resource{ID: res.ID, ExternalID: "mysql-fjord-abcxyz"}, "")
	if got != "Created add-on mysql-fjord-abcxyz and a new release." {
		t.Fatalf("external id: %q", got)
	}
	got = createdResourceMessage(&ct.Resource{ID: res.ID}, "")
	if got != "Created add-on 919a764d-863d-4629-aa23-76248728dcbc and a new release." {
		t.Fatalf("id fallback: %q", got)
	}
}

func TestResourceStartingMessage(t *testing.T) {
	res := &ct.Resource{Env: map[string]string{"FLYNN_REDIS": "redis-harbor-kxmnpq"}}
	got := resourceStartingMessage(res, "redis:wait")
	if got != "The instance is starting; check later with flynn redis:wait redis-harbor-kxmnpq." {
		t.Fatalf("redis: %q", got)
	}
	got = resourceStartingMessage(&ct.Resource{Env: map[string]string{"FLYNN_POSTGRES": "pg-orchid-xkhthp"}}, "pg:wait")
	if got != "The instance is starting; check later with flynn pg:wait pg-orchid-xkhthp." {
		t.Fatalf("postgres: %q", got)
	}
	got = resourceStartingMessage(&ct.Resource{Env: map[string]string{"FLYNN_KAFKA": "kafka-fjord-abcxyz"}}, "")
	if got != "The instance is starting; check later with flynn addons." {
		t.Fatalf("other: %q", got)
	}
}

func TestResourceDisplayNameAndMatch(t *testing.T) {
	res := &ct.Resource{
		ID:         "919a764d-863d-4629-aa23-76248728dcbc",
		ExternalID: "deadbeefdeadbeef",
		ProviderID: "prov-1",
		Env:        map[string]string{"FLYNN_POSTGRES": "pg-harbor-kxmnpq"},
	}
	if resourceDisplayName(res) != "pg-harbor-kxmnpq" {
		t.Fatalf("name %q", resourceDisplayName(res))
	}
	if !resourceMatchesRef(res, "pg-harbor-kxmnpq") || !resourceMatchesRef(res, res.ID) || !resourceMatchesRef(res, "deadbeefdeadbeef") {
		t.Fatal("match NAME, ID, and ExternalID")
	}
	if resourceMatchesRef(res, "other") {
		t.Fatal("unknown ref")
	}
}

type resourceLookupClient struct {
	controller.Client
	provider *ct.Provider
	appList  []*ct.Resource
	allList  []*ct.Resource
}

func (c resourceLookupClient) GetProvider(id string) (*ct.Provider, error) {
	if c.provider != nil && (id == c.provider.ID || id == c.provider.Name) {
		return c.provider, nil
	}
	return nil, controller.ErrNotFound
}

func (c resourceLookupClient) AppResourceList(string) ([]*ct.Resource, error) {
	return c.appList, nil
}

func (c resourceLookupClient) ResourceList(string) ([]*ct.Resource, error) {
	return c.allList, nil
}

func TestLookupAttachedResourceByNameNotProvider(t *testing.T) {
	arroyo := &ct.Resource{
		ID:         "c50874e7-8bec-4844-a51d-9a1d7ee9b87a",
		ProviderID: "prov-mysql",
		Env:        map[string]string{"FLYNN_MYSQL": "mysql-arroyo-64597"},
	}
	other := &ct.Resource{
		ID:         "11111111-1111-1111-1111-111111111111",
		ProviderID: "prov-mysql",
		Env:        map[string]string{"FLYNN_MYSQL": "mysql-basin-11111"},
	}
	c := resourceLookupClient{appList: []*ct.Resource{arroyo, other}}
	got, err := lookupAttachedResource(c, "demo", "mysql-arroyo-64597")
	if err != nil || got == nil || got.ID != arroyo.ID {
		t.Fatalf("name: %+v %v", got, err)
	}
	got, err = lookupAttachedResource(c, "demo", arroyo.ID)
	if err != nil || got == nil || got.ID != arroyo.ID {
		t.Fatalf("id: %+v %v", got, err)
	}
	_, err = lookupAttachedResource(c, "demo", "mysql")
	if err == nil || !strings.Contains(err.Error(), "not the provider") {
		t.Fatalf("provider name must be rejected: %v", err)
	}
}

func TestLookupProviderResourceByName(t *testing.T) {
	res := &ct.Resource{
		ID:         "919a764d-863d-4629-aa23-76248728dcbc",
		ProviderID: "prov-1",
		Env:        map[string]string{"FLYNN_POSTGRES": "pg-harbor-kxmnpq"},
	}
	c := resourceLookupClient{
		provider: &ct.Provider{ID: "prov-1", Name: "postgres"},
		appList:  []*ct.Resource{res},
	}
	got, err := lookupProviderResource(c, "shop", "postgres", "pg-harbor-kxmnpq")
	if err != nil || got == nil || got.ID != res.ID {
		t.Fatalf("by name: %+v %v", got, err)
	}
	peer, err := resolvePeerRef(c, "shop", "postgres", res.ID)
	if err != nil || peer != "pg-harbor-kxmnpq" {
		t.Fatalf("peer from ID: %q %v", peer, err)
	}
	if _, err := lookupProviderResource(c, "shop", "postgres", "missing"); err == nil {
		t.Fatal("missing must fail")
	}
}

func TestLookupProviderResourceFromAccountList(t *testing.T) {
	res := &ct.Resource{
		ID:           "919a764d-863d-4629-aa23-76248728dcbc",
		ProviderID:   "prov-1",
		OwnerAccount: "user:ada",
		OwnerApp:     "home",
		Env:          map[string]string{"FLYNN_POSTGRES": "pg-harbor-kxmnpq"},
	}
	c := resourceLookupClient{
		provider: &ct.Provider{ID: "prov-1", Name: "postgres"},
		appList:  nil,
		allList:  []*ct.Resource{res},
	}
	got, err := lookupProviderResource(c, "shop", "postgres", "pg-harbor-kxmnpq")
	if err != nil || got == nil || got.ID != res.ID {
		t.Fatalf("attach looks up a resource not yet on this app: %+v %v", got, err)
	}
}

func TestResolveRemoveTargetByNameOnly(t *testing.T) {
	res := &ct.Resource{
		ID:         "919a764d-863d-4629-aa23-76248728dcbc",
		ProviderID: "prov-1",
		Env:        map[string]string{"FLYNN_POSTGRES": "pg-orchid-xkhthp"},
	}
	c := resourceLookupClient{
		provider: &ct.Provider{ID: "prov-1", Name: "postgres"},
		appList:  []*ct.Resource{res},
	}
	got, provider, err := resolveRemoveTarget(c, "shop", "pg-orchid-xkhthp", "")
	if err != nil || got == nil || got.ID != res.ID || provider != "postgres" {
		t.Fatalf("name only: %+v %q %v", got, provider, err)
	}
	got, provider, err = resolveRemoveTarget(c, "shop", "postgres", "pg-orchid-xkhthp")
	if err != nil || got == nil || got.ID != res.ID || provider != "postgres" {
		t.Fatalf("provider+name: %+v %q %v", got, provider, err)
	}
}

func TestResourceFollowerNamesBlockLeader(t *testing.T) {
	leader := &ct.Resource{
		ID:  "leader",
		Env: map[string]string{"FLYNN_POSTGRES": "pg-orchid-xkhthp", "FLYNN_POSTGRES_ROLE": "primary"},
	}
	follower := &ct.Resource{
		ID:  "follower",
		Env: map[string]string{"FLYNN_POSTGRES": "pg-lagoon-abcdef", "FLYNN_POSTGRES_ROLE": "follower", "FLYNN_POSTGRES_LEADER": "pg-orchid-xkhthp"},
	}
	keys := []string{"FLYNN_POSTGRES_LEADER", "POSTGRES_LEADER"}
	names := resourceFollowerNames(leader, []*ct.Resource{leader, follower}, keys)
	if len(names) != 1 || names[0] != "pg-lagoon-abcdef" {
		t.Fatalf("followers %q", names)
	}
	if names := resourceFollowerNames(follower, []*ct.Resource{leader, follower}, keys); len(names) != 0 {
		t.Fatalf("follower itself %q", names)
	}
}

func TestResourceAddPostgresRejectsPlatformAppliance(t *testing.T) {
	if !pgappliance.IsPlatformApplianceURL("http://postgres-api.discoverd/databases") {
		t.Fatal("platform appliance URL must stay rejected by the controller")
	}
	if pgappliance.IsPlatformApplianceURL("http://postgres-plugin.discoverd/databases") {
		t.Fatal("tenant plugin URL must stay allowed")
	}
}

func TestResourceAddPostgresDoesNotScaleTheInstance(t *testing.T) {
	src, err := os.ReadFile("addons.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "ScaleAppRelease") {
		t.Fatal("addons:create must ProvisionResource on postgres-plugin; the plugin starts the instance")
	}
	if !strings.Contains(string(src), "ProvisionResource") {
		t.Fatal("addons:create must call ProvisionResource")
	}
	if !ct.ShouldProbeScaleStall(5 * time.Minute) {
		t.Fatal("a 5m ScaleAppRelease wait enables stall probes; the plugin must NoWait and poll discoverd")
	}
	if ct.ScaleStartingStuckTimeout >= time.Minute {
		t.Fatal("initdb typically exceeds ScaleStartingStuckTimeout")
	}
}

func TestResourceOwnedByApp(t *testing.T) {
	res := &ct.Resource{OwnerApp: "app-owner"}
	if res.OwnedByApp(&ct.App{ID: "app-other", Name: "shop"}) {
		t.Fatal("attached app must not own the resource")
	}
	if !res.OwnedByApp(&ct.App{ID: "app-owner", Name: "home"}) {
		t.Fatal("provisioning app owns the resource")
	}
	if !res.OwnedByApp(&ct.App{ID: "other", Name: "app-owner"}) {
		t.Fatal("owner_app may be the app name")
	}
	legacy := &ct.Resource{}
	if !legacy.OwnedByApp(&ct.App{ID: "any"}) {
		t.Fatal("empty owner_app is legacy-owned")
	}
	shared := &ct.Resource{Apps: []string{"app-peer", "app-owner"}}
	if shared.OwnedByApp(&ct.App{ID: "app-peer", Name: "shop"}) {
		t.Fatal("newest attached app must not own a legacy resource")
	}
	if !shared.OwnedByApp(&ct.App{ID: "app-owner", Name: "home"}) {
		t.Fatal("oldest attached app owns a legacy resource")
	}
}

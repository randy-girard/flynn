package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/pgappliance"
)

type postgresProviderClient struct {
	controller.Client
	provider *ct.Provider
	err      error
}

func (p postgresProviderClient) GetProvider(string) (*ct.Provider, error) {
	return p.provider, p.err
}

func TestCreatedResourceMessage(t *testing.T) {
	res := &ct.Resource{
		ID:         "919a764d-863d-4629-aa23-76248728dcbc",
		ExternalID: "deadbeefdeadbeef",
		Env:        map[string]string{"FLYNN_MYSQL": "mysql-harbor-kxmnpq"},
	}
	got := createdResourceMessage(res, "")
	if got != "Created resource mysql-harbor-kxmnpq and a new release." {
		t.Fatalf("name: %q", got)
	}
	got = createdResourceMessage(res, "analytics")
	if got != "Created resource mysql-harbor-kxmnpq (as ANALYTICS) and a new release." {
		t.Fatalf("as: %q", got)
	}
	got = createdResourceMessage(&ct.Resource{ID: res.ID, ExternalID: res.ExternalID}, "CACHE")
	if got != "Created resource CACHE and a new release." {
		t.Fatalf("as without env: %q", got)
	}
	got = createdResourceMessage(&ct.Resource{ID: res.ID, ExternalID: "mysql-fjord-abcxyz"}, "")
	if got != "Created resource mysql-fjord-abcxyz and a new release." {
		t.Fatalf("external id: %q", got)
	}
	got = createdResourceMessage(&ct.Resource{ID: res.ID}, "")
	if got != "Created resource 919a764d-863d-4629-aa23-76248728dcbc and a new release." {
		t.Fatalf("id fallback: %q", got)
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

func (c resourceLookupClient) GetProvider(string) (*ct.Provider, error) {
	return c.provider, nil
}

func (c resourceLookupClient) AppResourceList(string) ([]*ct.Resource, error) {
	return c.appList, nil
}

func (c resourceLookupClient) ResourceList(string) ([]*ct.Resource, error) {
	return c.allList, nil
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

func TestResourceAddPostgresRejectsPlatformAppliance(t *testing.T) {
	err := rejectPlatformPostgresAdd("postgres", postgresProviderClient{err: controller.ErrNotFound})
	if !errors.Is(err, pgappliance.ErrTenantProvision) {
		t.Fatalf("missing provider: %v", err)
	}
	err = rejectPlatformPostgresAdd("postgres", postgresProviderClient{
		provider: &ct.Provider{Name: "postgres", URL: "http://postgres-api.discoverd/databases"},
	})
	if !errors.Is(err, pgappliance.ErrTenantProvision) {
		t.Fatalf("platform provider: %v", err)
	}
}

func TestResourceAddPostgresDoesNotScaleTheInstance(t *testing.T) {
	src, err := os.ReadFile("resource.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "ScaleAppRelease") {
		t.Fatal("resource:add must ProvisionResource on postgres-plugin; the plugin starts the instance")
	}
	if !strings.Contains(string(src), "ProvisionResource") {
		t.Fatal("resource:add must call ProvisionResource")
	}
	if !ct.ShouldProbeScaleStall(5 * time.Minute) {
		t.Fatal("a 5m ScaleAppRelease wait enables stall probes; the plugin must NoWait and poll discoverd")
	}
	if ct.ScaleStartingStuckTimeout >= time.Minute {
		t.Fatal("initdb typically exceeds ScaleStartingStuckTimeout")
	}
}

func TestResourceAddPostgresAllowsPluginProvider(t *testing.T) {
	err := rejectPlatformPostgresAdd("postgres", postgresProviderClient{
		provider: &ct.Provider{Name: "postgres", URL: "http://postgres-plugin.discoverd/databases"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = rejectPlatformPostgresAdd("postgres", postgresProviderClient{
		provider: &ct.Provider{Name: "postgres", URL: "http://plugin-postgres-api.discoverd/databases"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = rejectPlatformPostgresAdd("mysql", postgresProviderClient{err: controller.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSingleAttachmentEnv(t *testing.T) {
	env, err := singleAttachmentEnv(map[string]string{"DATABASE_URL": "postgres://db"}, "ANALYTICS")
	if err != nil || len(env) != 1 || env["ANALYTICS_URL"] != "postgres://db" {
		t.Fatalf("env %#v %v", env, err)
	}
	env, err = singleAttachmentEnv(map[string]string{"DATABASE_URL": "postgres://db"}, "")
	if err != nil || env["DATABASE_URL"] != "postgres://db" || len(env) != 1 {
		t.Fatalf("default %#v %v", env, err)
	}
}

func TestDatabaseProvisionConfigUsesRuntime(t *testing.T) {
	cat := dbruntime.BuiltinCatalog()
	got, err := databaseProvisionConfig("scheduler", "ANALYTICS", "res", "", "perf-l", "logical", "", "", "", cat)
	if err != nil || got != nil {
		t.Fatalf("non-database config %s %v", got, err)
	}
	got, err = databaseProvisionConfig("redis", "", "", "", "", "", "", "", "", cat)
	if err != nil || got == nil {
		t.Fatalf("redis default %s %v", got, err)
	}
	var redis databaseProvisionBody
	if err := json.Unmarshal(*got, &redis); err != nil {
		t.Fatal(err)
	}
	small, _ := cat.Find(dbruntime.EngineRedis, "small")
	if redis.Runtime != "small" || redis.Disk != small.Disk || redis.CPU != small.CPU || redis.Memory != small.Memory {
		t.Fatalf("redis small %#v", redis)
	}
	got, err = databaseProvisionConfig("postgres", "", "", "", "", "", "", "", "", cat)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	var pg databaseProvisionBody
	if err := json.Unmarshal(*got, &pg); err != nil {
		t.Fatal(err)
	}
	pgSmall, _ := cat.Find(dbruntime.EnginePostgres, "small")
	if pg.Disk != pgSmall.Disk || pg.Disk <= redis.Disk {
		t.Fatalf("postgres disk %d redis disk %d", pg.Disk, redis.Disk)
	}
	got, err = databaseProvisionConfig("mysql", "REPLICA", "mysql-harbor-kxmnpq", "", "", "", "", "", "", cat)
	if err != nil || got == nil || !strings.Contains(string(*got), `"follow":"mysql-harbor-kxmnpq"`) {
		t.Fatalf("mysql follow %s %v", got, err)
	}
	if strings.Contains(string(*got), `"join"`) {
		t.Fatalf("mysql must not send join %s", *got)
	}
	got, err = databaseProvisionConfig("kafka", "", "", "kafka-harbor-aaaaaa", "", "", "", "", "", cat)
	if err != nil || got == nil || !strings.Contains(string(*got), `"join":"kafka-harbor-aaaaaa"`) || !strings.Contains(string(*got), `"follow":"kafka-harbor-aaaaaa"`) {
		t.Fatalf("kafka join %s %v", got, err)
	}
	got, err = databaseProvisionConfig("mongodb", "", "mongodb-cedar-azkmls", "", "", "", "", "", "", cat)
	if err != nil || got == nil || !strings.Contains(string(*got), `"join":"mongodb-cedar-azkmls"`) {
		t.Fatalf("mongodb follow alias %s %v", got, err)
	}
	_, err = databaseProvisionConfig("postgres", "", "", "pg-harbor-aaaaaa", "", "", "", "", "", cat)
	if err == nil || !strings.Contains(err.Error(), "--join") {
		t.Fatalf("postgres join: %v", err)
	}
	_, err = databaseProvisionConfig("kafka", "", "a", "b", "", "", "", "", "", cat)
	if err == nil || !strings.Contains(err.Error(), "cannot both") {
		t.Fatalf("join and follow: %v", err)
	}
	got, err = databaseProvisionConfig("postgres", "ANALYTICS", "leader", "", "medium", "logical", "", "", "", cat)
	if err != nil || got == nil || !strings.Contains(string(*got), `"as":"ANALYTICS"`) || !strings.Contains(string(*got), `"follow":"leader"`) {
		t.Fatalf("config %s %v", got, err)
	}
	med, _ := cat.Find(dbruntime.EnginePostgres, "medium")
	var sized databaseProvisionBody
	if err := json.Unmarshal(*got, &sized); err != nil {
		t.Fatal(err)
	}
	if sized.Runtime != "medium" || sized.Disk != med.Disk || sized.Replication != "logical" {
		t.Fatalf("medium %#v", sized)
	}
	_, err = databaseProvisionConfig("mysql", "", "", "", "cache", "", "", "", "", cat)
	var unpublished *dbruntime.UnpublishedError
	if !errors.As(err, &unpublished) {
		t.Fatalf("unpublished runtime: %v", err)
	}
	_, err = databaseProvisionConfig("redis", "", "", "", "", "", "100", "128MB", "1GB", cat)
	if !errors.Is(err, dbruntime.ErrCustomSizesDisabled) {
		t.Fatalf("raw size: %v", err)
	}
	cat.AllowCustomSizes = true
	got, err = databaseProvisionConfig("redis", "", "", "", "", "", "100", "128MB", "1GB", cat)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	var custom databaseProvisionBody
	if err := json.Unmarshal(*got, &custom); err != nil {
		t.Fatal(err)
	}
	if custom.Runtime != "custom" || custom.CPU != 100 || custom.Disk != 1<<30 {
		t.Fatalf("custom %#v", custom)
	}
}

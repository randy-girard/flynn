package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
	got, err := databaseProvisionConfig("scheduler", "ANALYTICS", "res", "perf-l", "logical", "", "", "", cat)
	if err != nil || got != nil {
		t.Fatalf("non-database config %s %v", got, err)
	}
	got, err = databaseProvisionConfig("redis", "", "", "", "", "", "", "", cat)
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
	got, err = databaseProvisionConfig("postgres", "", "", "", "", "", "", "", cat)
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
	got, err = databaseProvisionConfig("postgres", "ANALYTICS", "leader", "medium", "logical", "", "", "", cat)
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
	_, err = databaseProvisionConfig("mysql", "", "", "cache", "", "", "", "", cat)
	var unpublished *dbruntime.UnpublishedError
	if !errors.As(err, &unpublished) {
		t.Fatalf("unpublished runtime: %v", err)
	}
	_, err = databaseProvisionConfig("redis", "", "", "", "", "100", "128MB", "1GB", cat)
	if !errors.Is(err, dbruntime.ErrCustomSizesDisabled) {
		t.Fatalf("raw size: %v", err)
	}
	cat.AllowCustomSizes = true
	got, err = databaseProvisionConfig("redis", "", "", "", "", "100", "128MB", "1GB", cat)
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

package main

import (
	"errors"
	"strings"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
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

func TestPostgresProvisionConfig(t *testing.T) {
	got, err := postgresProvisionConfig("redis", "ANALYTICS", "res", "perf-l", "logical")
	if err != nil || got != nil {
		t.Fatalf("non-postgres config %s %v", got, err)
	}
	got, err = postgresProvisionConfig("postgres", "", "", "", "")
	if err != nil || got != nil {
		t.Fatalf("empty config %s %v", got, err)
	}
	got, err = postgresProvisionConfig("postgres", "ANALYTICS", "leader", "perf-l", "logical")
	if err != nil || got == nil || !strings.Contains(string(*got), `"as":"ANALYTICS"`) || !strings.Contains(string(*got), `"follow":"leader"`) {
		t.Fatalf("config %s %v", got, err)
	}
}

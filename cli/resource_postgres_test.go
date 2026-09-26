package main

import (
	"errors"
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

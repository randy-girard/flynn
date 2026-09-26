package pgappliance

import (
	"errors"
	"strings"
	"testing"
)

func TestAllowProvisionRejectsTenantWithoutSQL(t *testing.T) {
	for _, body := range [][]byte{nil, {}, []byte(`{}`), []byte(`{"platform":false}`), []byte(`nope`)} {
		err := AllowProvision(body)
		if !errors.Is(err, ErrTenantProvision) {
			t.Fatalf("body %q: %v", body, err)
		}
		if strings.Contains(err.Error(), "CREATE USER") || strings.Contains(err.Error(), "CREATE DATABASE") {
			t.Fatalf("tenant error must not be SQL: %v", err)
		}
		if !strings.Contains(err.Error(), "flynn-plugin-postgres") {
			t.Fatalf("tenant error: %v", err)
		}
	}
}

func TestAllowProvisionAcceptsPlatformMarker(t *testing.T) {
	if err := AllowProvision([]byte(PlatformProvisionBody)); err != nil {
		t.Fatal(err)
	}
	if err := AllowProvision([]byte(" { \"platform\" : true } ")); err != nil {
		t.Fatal(err)
	}
}

func TestSystemProvisionBody(t *testing.T) {
	body, err := SystemProvisionBody(true)
	if err != nil || string(body) != PlatformProvisionBody {
		t.Fatalf("system body %q %v", body, err)
	}
	body, err = SystemProvisionBody(false)
	if !errors.Is(err, ErrTenantProvision) || body != nil {
		t.Fatalf("tenant body %q %v", body, err)
	}
}

func TestIsPlatformApplianceURL(t *testing.T) {
	if !IsPlatformApplianceURL("http://postgres-api.discoverd/databases") {
		t.Fatal("platform URL")
	}
	if !IsPlatformApplianceURL("http://postgres-api.discoverd:3000/databases") {
		t.Fatal("platform URL with port")
	}
	for _, raw := range []string{
		"http://redis-api.discoverd/clusters",
		"http://postgres.discoverd/databases",
		"http://plugin-postgres-api.discoverd/databases",
		"",
	} {
		if IsPlatformApplianceURL(raw) {
			t.Fatalf("not the platform appliance: %s", raw)
		}
	}
}

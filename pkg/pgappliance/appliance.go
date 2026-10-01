// Package pgappliance distinguishes the built-in platform Postgres appliance
// from tenant Postgres (the upcoming flynn-plugin-postgres).
package pgappliance

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
)

// PlatformApplianceHost is the discoverd service for the system Postgres API.
// Tenant plugins must use a different host so they can register their own provider.
const PlatformApplianceHost = "postgres-api.discoverd"

// PlatformProvisionBody is the JSON bootstrap sends when creating a database
// for a system app (controller, blobstore). Other bodies are tenant provisions.
const PlatformProvisionBody = `{"platform":true}`

// ErrTenantProvision is returned when a request would create a role on the
// platform appliance for a user app (not system or plugin).
var ErrTenantProvision = errors.New("tenant Postgres is not the platform appliance; install the postgres plugin (flynn-plugin-postgres) and run flynn resource:add postgres")

// IsPlatformApplianceURL reports whether raw is the built-in appliance API.
func IsPlatformApplianceURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Hostname() == PlatformApplianceHost
}

// AllowProvision accepts only the platform marker. A tenant body returns
// ErrTenantProvision and the caller must not build CREATE USER / CREATE DATABASE SQL.
func AllowProvision(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return ErrTenantProvision
	}
	var req struct {
		Platform bool `json:"platform"`
	}
	if err := json.Unmarshal(body, &req); err != nil || !req.Platform {
		return ErrTenantProvision
	}
	return nil
}

// AllowPlatformApp is true when the consumer may get a database on the
// built-in appliance (controller, blobstore, plugin apps). User apps must
// install flynn-plugin-postgres.
func AllowPlatformApp(system, plugin bool) bool {
	return system || plugin
}

// SystemProvisionBody is the appliance request for a system or plugin app.
// User apps get ErrTenantProvision and no request body.
func SystemProvisionBody(system bool) ([]byte, error) {
	if !system {
		return nil, ErrTenantProvision
	}
	return []byte(PlatformProvisionBody), nil
}

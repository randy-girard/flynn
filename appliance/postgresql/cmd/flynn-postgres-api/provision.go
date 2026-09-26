package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/randy-girard/flynn/pkg/pgappliance"
)

// TenantConnectionLimit is the CONNECTION LIMIT on each provisioned role.
const TenantConnectionLimit = 20

// ProvisionPlan is the SQL for one database on the platform appliance.
// Maintenance statements run on the postgres maintenance database. TenantDB
// statements run inside the new database as the flynn superuser and block
// CREATE EXTENSION for every role except flynn. Tenant apps never receive
// this plan; only the platform marker (controller, blobstore) does.
type ProvisionPlan struct {
	Maintenance []string
	TenantDB    []string
}

// planProvision builds system-database SQL only after the platform marker is
// accepted. A tenant body returns before any CREATE USER / CREATE DATABASE text.
func planProvision(body []byte, username, password, database string) (ProvisionPlan, error) {
	if err := pgappliance.AllowProvision(body); err != nil {
		return ProvisionPlan{}, err
	}
	return BuildProvisionPlan(username, password, database, TenantConnectionLimit), nil
}

// rejectSuperuserPassword refuses to publish the appliance superuser password
// as an app role password.
func rejectSuperuserPassword(password, superuser string) error {
	if superuser != "" && password == superuser {
		return errors.New("refusing to inject the appliance superuser password")
	}
	return nil
}

// provisionEnv is the app environment for a role created on the appliance.
// It never copies the appliance superuser password.
func provisionEnv(service, host, username, password, database, superuser string) (map[string]string, error) {
	if err := rejectSuperuserPassword(password, superuser); err != nil {
		return nil, err
	}
	return map[string]string{
		"FLYNN_POSTGRES": service,
		"PGHOST":         host,
		"PGUSER":         username,
		"PGPASSWORD":     password,
		"PGDATABASE":     database,
		"PGSSLMODE":      "require",
		"DATABASE_URL":   databaseURL(username, password, host, database),
	}, nil
}

// BuildProvisionPlan returns the provision SQL. Passwords are SQL literals.
func BuildProvisionPlan(username, password, database string, limit int) ProvisionPlan {
	if limit <= 0 {
		limit = TenantConnectionLimit
	}
	user := quoteIdent(username)
	db := quoteIdent(database)
	pass := quoteLiteral(password)
	role := fmt.Sprintf(`CREATE USER %s WITH PASSWORD %s CONNECTION LIMIT %d NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION`, user, pass, limit)
	return ProvisionPlan{
		Maintenance: []string{
			role,
			fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, db, user),
			revokeConnectSQL(database),
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, user),
			fmt.Sprintf(`REVOKE CONNECT ON DATABASE "postgres" FROM %s`, user),
		},
		TenantDB: blockCreateExtensionSQL(),
	}
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// blockCreateExtensionSQL installs an event trigger that rejects CREATE
// EXTENSION unless the current user is the flynn superuser.
func blockCreateExtensionSQL() []string {
	return []string{
		`CREATE OR REPLACE FUNCTION flynn_block_create_extension() RETURNS event_trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF current_user <> 'flynn' THEN
    RAISE EXCEPTION 'CREATE EXTENSION is not allowed for tenant roles';
  END IF;
END;
$fn$`,
		`DROP EVENT TRIGGER IF EXISTS flynn_block_create_extension`,
		`CREATE EVENT TRIGGER flynn_block_create_extension ON ddl_command_start WHEN TAG IN ('CREATE EXTENSION') EXECUTE FUNCTION flynn_block_create_extension()`,
	}
}

package main

import (
	"fmt"
	"strings"
)

// TenantConnectionLimit is the CONNECTION LIMIT on each provisioned role.
const TenantConnectionLimit = 20

// ProvisionPlan is the SQL for one tenant database. Maintenance statements
// run on the postgres maintenance database. TenantDB statements run inside
// the new database as the flynn superuser and block CREATE EXTENSION for
// every role except flynn (untrusted extensions already require superuser;
// the event trigger also rejects trusted extensions).
type ProvisionPlan struct {
	Maintenance []string
	TenantDB    []string
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

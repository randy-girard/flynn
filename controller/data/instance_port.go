package data

import (
	"fmt"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/instanceport"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// instancePortFromEnvSQL is the integer port in a resource's
// FLYNN_INSTANCE_PORT env, or NULL when it is missing or not a decimal port.
// The CASE keeps the cast from running on a value that is not a number.
const instancePortFromEnvSQL = `(CASE WHEN btrim(env->>'` + instanceport.EnvPort + `') ~ '^[0-9]{1,5}$'
  AND btrim(env->>'` + instanceport.EnvPort + `')::int BETWEEN 1 AND 65535
  THEN btrim(env->>'` + instanceport.EnvPort + `')::int END)`

// instancePortKey is the unique index over the live rows' instance_port.
const instancePortKey = "resources_instance_port_key"

// migrateInstancePorts backs resources.instance_port (migration 97). The
// oldest live resource of each port gets it. A port two live resources
// already shared stays in the env of the others (their running instance
// keeps it) and is handed to the next one when the holder is deleted.
var migrateInstancePorts = []string{
	`ALTER TABLE resources ADD COLUMN instance_port integer`,
	`UPDATE resources r SET instance_port = v.port FROM (
		SELECT DISTINCT ON (port) resource_id, port FROM (
			SELECT resource_id, created_at, ` + instancePortFromEnvSQL + ` AS port
			FROM resources WHERE deleted_at IS NULL AND jsonb_typeof(env) = 'object'
		) p WHERE port IS NOT NULL
		ORDER BY port, created_at, resource_id
	) v WHERE r.resource_id = v.resource_id`,
	`CREATE UNIQUE INDEX ` + instancePortKey + ` ON resources (instance_port) WHERE deleted_at IS NULL`,
}

// envInstancePort is the port env names, or nil.
func envInstancePort(env map[string]string) *int {
	if env == nil {
		return nil
	}
	port, ok := instanceport.ParsePort(env[instanceport.EnvPort])
	if !ok {
		return nil
	}
	return &port
}

// instancePortError turns the unique-index violation into a validation error
// that names the port.
func instancePortError(err error, port *int) error {
	if port != nil && postgres.IsUniquenessError(err, instancePortKey) {
		return ct.ValidationError{Field: "env", Message: fmt.Sprintf("instance port %d is already assigned to another resource", *port)}
	}
	return err
}

func lockInstancePorts(tx *postgres.DBTx) error {
	return tx.QueryRow("SELECT pg_advisory_xact_lock($1)::text", instanceport.AllocLock).Scan(new(string))
}

// AddWithInstancePort inserts r with an instance port that no other live
// resource holds. assign gets the held ports (port to the holder's external
// id) and returns the env to store. The allocation runs under a transaction
// advisory lock that is held until the row commits, so two provisions, on
// one controller or several, never get the same port. The unique index on
// instance_port is the backstop for writes that skip the lock.
func (rr *ResourceRepo) AddWithInstancePort(r *ct.Resource, assign func(used map[int]string) (map[string]string, error)) error {
	tx, err := rr.db.Begin()
	if err != nil {
		return err
	}
	if err := lockInstancePorts(tx); err != nil {
		tx.Rollback()
		return err
	}
	used, err := instancePortsHeld(tx)
	if err != nil {
		tx.Rollback()
		return err
	}
	env, err := assign(used)
	if err != nil {
		tx.Rollback()
		return err
	}
	r.Env = env
	if err := rr.add(tx, r); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func instancePortsHeld(tx *postgres.DBTx) (map[int]string, error) {
	rows, err := tx.Query("resource_instance_port_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]string{}
	for rows.Next() {
		var port int
		var externalID string
		if err := rows.Scan(&port, &externalID); err != nil {
			return nil, err
		}
		used[port] = externalID
	}
	return used, rows.Err()
}

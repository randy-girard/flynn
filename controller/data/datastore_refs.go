package data

import (
	"github.com/randy-girard/flynn/pkg/datastoreguard"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// DatastoreRefSource is the app-deletion datastore guard's view of the
// cluster, read from the controller database in one query.
type DatastoreRefSource struct {
	db *postgres.DB
}

func NewDatastoreRefSource(db *postgres.DB) *DatastoreRefSource {
	return &DatastoreRefSource{db: db}
}

// AppRefs lists every live app with its owner account, current release env
// and the env of its attached resources.
func (s *DatastoreRefSource) AppRefs() ([]datastoreguard.AppRef, error) {
	rows, err := s.db.Query("datastore_guard_app_refs")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []datastoreguard.AppRef
	for rows.Next() {
		var ref datastoreguard.AppRef
		if err := rows.Scan(&ref.ID, &ref.Name, &ref.OwnerAccount, &ref.Env, &ref.ResourceEnvs); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

package data

import (
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// DBRuntimeRepo stores database instance sizes in cluster postgres.
// App process runtimes live in runtime_profiles; these are per-engine
// resource:add sizes. GET /db-runtimes reads this table on every request.
type DBRuntimeRepo struct {
	db *postgres.DB
}

func NewDBRuntimeRepo(db *postgres.DB) *DBRuntimeRepo {
	return &DBRuntimeRepo{db: db}
}

func (r *DBRuntimeRepo) Load() (dbruntime.Catalog, error) {
	cat := dbruntime.EmptyCatalog()
	if err := r.db.QueryRow("db_runtime_settings_select").Scan(&cat.AllowCustomSizes); err != nil {
		return dbruntime.Catalog{}, err
	}
	rows, err := r.db.Query("db_runtime_list")
	if err != nil {
		return dbruntime.Catalog{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var rt dbruntime.Runtime
		if err := rows.Scan(&rt.Engine, &rt.Name, &rt.CPU, &rt.Memory, &rt.Disk, &rt.Builtin); err != nil {
			return dbruntime.Catalog{}, err
		}
		cat.Runtimes = append(cat.Runtimes, rt)
	}
	return cat, rows.Err()
}

// Replace writes the full catalog. An empty catalog is a real empty list,
// not a no-op.
func (r *DBRuntimeRepo) Replace(cat dbruntime.Catalog) error {
	_, err := r.mutate(func(*dbruntime.Catalog) error {
		return nil
	}, &cat)
	return err
}

// Mutate loads the catalog, applies fn, and writes the result in one transaction.
func (r *DBRuntimeRepo) Mutate(fn func(*dbruntime.Catalog) error) (dbruntime.Catalog, error) {
	return r.mutate(fn, nil)
}

func (r *DBRuntimeRepo) mutate(fn func(*dbruntime.Catalog) error, replace *dbruntime.Catalog) (dbruntime.Catalog, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return dbruntime.Catalog{}, err
	}
	defer tx.Rollback()

	var locked bool
	if err := tx.QueryRow("db_runtime_settings_lock").Scan(&locked); err != nil {
		return dbruntime.Catalog{}, err
	}

	cat := dbruntime.EmptyCatalog()
	if replace != nil {
		cat = *replace
	} else {
		cat.AllowCustomSizes = locked
		rows, err := tx.Query("db_runtime_list")
		if err != nil {
			return dbruntime.Catalog{}, err
		}
		for rows.Next() {
			var rt dbruntime.Runtime
			if err := rows.Scan(&rt.Engine, &rt.Name, &rt.CPU, &rt.Memory, &rt.Disk, &rt.Builtin); err != nil {
				rows.Close()
				return dbruntime.Catalog{}, err
			}
			cat.Runtimes = append(cat.Runtimes, rt)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return dbruntime.Catalog{}, err
		}
		if fn != nil {
			if err := fn(&cat); err != nil {
				return dbruntime.Catalog{}, err
			}
		}
	}

	if err := tx.Exec("db_runtime_delete_all"); err != nil {
		return dbruntime.Catalog{}, err
	}
	for _, rt := range cat.Runtimes {
		if err := tx.Exec("db_runtime_insert", rt.Engine, rt.Name, rt.CPU, rt.Memory, rt.Disk, rt.Builtin); err != nil {
			return dbruntime.Catalog{}, err
		}
	}
	if err := tx.Exec("db_runtime_settings_update", cat.AllowCustomSizes); err != nil {
		return dbruntime.Catalog{}, err
	}
	if err := tx.Commit(); err != nil {
		return dbruntime.Catalog{}, err
	}
	return cat, nil
}

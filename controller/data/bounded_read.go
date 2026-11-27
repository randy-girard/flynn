package data

import (
	"fmt"
	"time"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// streamListTimeout bounds, on the server, each query that builds the first
// list of a scheduler stream (formations, volumes). A list blocked on a lock
// fails with query_canceled (57014) instead of keeping the stream open with
// keepalives; the stream then ends with that error and the client reconnects.
var streamListTimeout = 30 * time.Second

// rowsQuerier is a pool or a transaction.
type rowsQuerier interface {
	Query(sql string, args ...interface{}) (*pgx.Rows, error)
}

// beginBoundedRead starts a transaction whose statements each fail after d.
// SET LOCAL ends with the transaction, so the pooled connection keeps its
// default timeout afterwards. Callers only read; Rollback ends it.
func beginBoundedRead(db *postgres.DB, d time.Duration) (*postgres.DBTx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	ms := d.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", ms)); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

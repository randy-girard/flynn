package backend

import (
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/postgres"
)

var ErrNotFound = errors.New("file not found")

type File struct {
	FileStream
	FileInfo
}

type FileStream interface {
	io.ReadSeeker
	io.Closer
}

type Redirector interface {
	RedirectURL() string
}

type FileInfo struct {
	ID         string
	Name       string
	Size       int64
	ETag       string
	Type       string
	Oid        *pgx.Oid
	ExternalID string
	ModTime    time.Time
}

type Backend interface {
	Name() string
	Open(tx *postgres.DBTx, info FileInfo, txControl bool) (FileStream, error)
	// Put writes r to the object info names: info.Oid (a large object the
	// caller created) for the Postgres backend, info.ExternalID for the
	// others. The caller chooses that id and records it before the upload,
	// so a failed upload can be removed. Put runs with no transaction open
	// and must not hold one while it reads r, which is often a client's
	// request body: a slow client would keep a pool connection busy for as
	// long as it takes (EDGE-33). An append is a new object that starts with
	// the old data, so Put always writes a whole object.
	Put(db *postgres.DB, info FileInfo, r io.Reader) error
	Copy(tx *postgres.DBTx, dst, src FileInfo) error
	Delete(tx *postgres.DBTx, info FileInfo) error
}

var Backends = make(map[string]func(string, map[string]string) (Backend, error))

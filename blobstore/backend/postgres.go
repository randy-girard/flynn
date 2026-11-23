package backend

import (
	"errors"
	"io"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/postgres"
)

var Postgres Backend = pg{}

type pg struct{}

func (pg) Name() string { return "postgres" }

// pgChunkSize is how much of a body the Postgres backend buffers before it
// writes it to the large object.
const pgChunkSize = 1 << 20

// Put writes r to the large object info.Oid in chunks. Each chunk is one
// lo_put statement in its own implicit transaction, so no transaction or pool
// connection is held while r (a client's body) is read. The object is not
// visible to readers until the caller commits a files row that names it.
func (p pg) Put(db *postgres.DB, info FileInfo, r io.Reader) error {
	if info.Oid == nil {
		return errors.New("blobstore: postgres put without a large object")
	}
	buf := make([]byte, pgChunkSize)
	var off int64
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if err := db.Exec("SELECT lo_put($1, $2, $3)", *info.Oid, off, buf[:n]); err != nil {
				return err
			}
			off += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// putTx writes r to a new large object for the row info.ID inside tx. Copy
// uses it: its source is already stored, not a client's body.
func (p pg) putTx(tx *postgres.DBTx, info FileInfo, r io.Reader) error {
	if err := tx.QueryRow("UPDATE files SET file_oid = lo_create(0) WHERE file_id = $1 RETURNING file_oid", info.ID).Scan(&info.Oid); err != nil {
		return err
	}
	lo, err := tx.LargeObjects()
	if err != nil {
		return err
	}
	obj, err := lo.Open(*info.Oid, pgx.LargeObjectModeWrite)
	if err != nil {
		return err
	}
	_, err = io.Copy(obj, r)
	return err
}

func (p pg) Copy(tx *postgres.DBTx, dst, src FileInfo) error {
	srcFile, err := p.Open(tx, src, false)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	return p.putTx(tx, dst, srcFile)
}

// NewLargeObjectReader reads size bytes of the large object oid with lo_get,
// one chunk per statement, so it holds no transaction between reads. An
// append reads the old data with it while the new body arrives.
func NewLargeObjectReader(db *postgres.DB, oid pgx.Oid, size int64) io.Reader {
	return &loReader{db: db, oid: oid, size: size}
}

type loReader struct {
	db   *postgres.DB
	oid  pgx.Oid
	off  int64
	size int64
}

func (l *loReader) Read(p []byte) (int, error) {
	if l.off >= l.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > pgChunkSize {
		n = pgChunkSize
	}
	if rest := l.size - l.off; n > rest {
		n = rest
	}
	var chunk []byte
	if err := l.db.QueryRow("SELECT lo_get($1, $2, $3)", l.oid, l.off, int32(n)).Scan(&chunk); err != nil {
		return 0, err
	}
	if len(chunk) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	l.off += int64(len(chunk))
	return copy(p, chunk), nil
}

func (p pg) Delete(tx *postgres.DBTx, info FileInfo) error {
	if err := tx.Exec("SELECT lo_unlink($1)", info.Oid); err != nil {
		return err
	}
	return tx.Exec("UPDATE files SET file_oid = NULL WHERE file_id = $1", info.ID)
}

func (p pg) Open(tx *postgres.DBTx, info FileInfo, txControl bool) (FileStream, error) {
	if info.Oid == nil {
		return nil, ErrNotFound
	}

	lo, err := tx.LargeObjects()
	if err != nil {
		return nil, err
	}
	obj, err := lo.Open(*info.Oid, pgx.LargeObjectModeRead)
	if err != nil {
		return nil, err
	}
	f := &pgFile{LargeObject: obj, size: info.Size}
	if txControl {
		f.tx = tx
	}
	return f, nil
}

type pgFile struct {
	*pgx.LargeObject
	size     int64
	sizeRead bool
	tx       *postgres.DBTx
}

func (f *pgFile) Close() error {
	if f.tx != nil {
		f.tx.Rollback()
	}
	return nil
}

func (f *pgFile) Seek(offset int64, whence int) (int64, error) {
	// HACK: work around ServeContent length detection, remove when fixed
	if offset == 0 && whence == io.SeekEnd {
		f.sizeRead = true
		return f.size, nil
	} else if f.sizeRead && offset == 0 && whence == io.SeekStart {
		return 0, nil
	}
	return f.LargeObject.Seek(offset, whence)
}

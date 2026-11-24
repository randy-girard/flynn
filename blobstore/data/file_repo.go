package data

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/stevvooe/resumable"
	"github.com/stevvooe/resumable/sha512"
)

func NewFileRepoFromEnv(db *postgres.DB) (*FileRepo, error) {
	backends := []backend.Backend{backend.Postgres}
	environment := os.Environ()
	for _, env := range environment {
		nameInfo := strings.SplitN(env, "=", 2)
		nameMatch := configEnvPattern.FindStringSubmatch(nameInfo[0])
		if len(nameMatch) < 2 {
			continue
		}
		name := strings.ToLower(nameMatch[1])
		info, err := parseBackendInfo(environment, name, nameInfo[1])
		if err != nil {
			return nil, err
		}
		newBackend, ok := backend.Backends[info["backend"]]
		if !ok {
			return nil, fmt.Errorf("blobstore: unknown backend %q for %s", info["backend"], name)
		}
		b, err := newBackend(name, info)
		if err != nil {
			return nil, err
		}
		log.Printf("Configured additional backend: %s (%s)\n", name, info["backend"])
		backends = append(backends, b)
	}

	defaultBackend := "postgres"
	if d := os.Getenv("DEFAULT_BACKEND"); d != "" {
		defaultBackend = d
		var found bool
		for _, b := range backends {
			if b.Name() == d {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("error: unknown default backend %q", d)
		}
	}
	return NewFileRepo(db, backends, defaultBackend), nil
}

type FileRepo struct {
	db             *postgres.DB
	backends       map[string]backend.Backend
	defaultBackend backend.Backend
}

func NewFileRepo(db *postgres.DB, backends []backend.Backend, defaultBackend string) *FileRepo {
	r := &FileRepo{
		db:       db,
		backends: make(map[string]backend.Backend, len(backends)),
	}
	for _, b := range backends {
		r.backends[b.Name()] = b
	}
	r.defaultBackend = r.backends[defaultBackend]
	return r
}

func (r *FileRepo) getBackend(name string) (backend.Backend, error) {
	if b, ok := r.backends[name]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("blobstore: unknown backend %q", name)
}

func (r *FileRepo) DefaultBackend() backend.Backend {
	return r.defaultBackend
}

func (r *FileRepo) List(dir string) ([]string, error) {
	rows, err := r.db.Query("SELECT substring(name FROM '^' || $1 || '/[^/]+/?') AS path FROM files WHERE name LIKE $1 || '%' AND deleted_at IS NULL GROUP BY path", strings.TrimSuffix(dir, "/"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path *string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if path != nil {
			paths = append(paths, *path)
		}
	}
	return paths, rows.Err()
}

type BackendFile struct {
	backend.Backend
	backend.FileInfo
}

func (r *FileRepo) ListFilesExcludingDefaultBackend(prefix string) ([]BackendFile, error) {
	rows, err := r.db.Query(
		"SELECT file_id, file_oid, external_id, backend, name, type, size, sha512, updated_at FROM files WHERE backend != $1 AND NAME LIKE $2 || '%' AND deleted_at IS NULL",
		r.defaultBackend.Name(), prefix,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []BackendFile
	for rows.Next() {
		var info backend.FileInfo
		var backendName string
		var externalID *string
		var sha512 []byte
		// size is NULL on rows carried over from the first schema
		var size *int64
		if err := rows.Scan(&info.ID, &info.Oid, &externalID, &backendName, &info.Name, &info.Type, &size, &sha512, &info.ModTime); err != nil {
			return nil, err
		}
		if externalID != nil {
			info.ExternalID = *externalID
		}
		if size != nil {
			info.Size = *size
		}
		info.ETag = base64.StdEncoding.EncodeToString(sha512)
		f := BackendFile{FileInfo: info}
		f.Backend, _ = r.getBackend(backendName)
		res = append(res, f)
	}

	return res, rows.Err()
}

func (r *FileRepo) ListDeletedFilesForCleanup() ([]BackendFile, error) {
	rows, err := r.db.Query(
		"SELECT file_id, external_id, backend, name FROM files WHERE backend != 'postgres' AND deleted_at IS NOT NULL",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []BackendFile
	for rows.Next() {
		var info backend.FileInfo
		var backendName string
		var externalID *string
		if err := rows.Scan(&info.ID, &externalID, &backendName, &info.Name); err != nil {
			return nil, err
		}
		if externalID != nil {
			info.ExternalID = *externalID
		}
		f := BackendFile{FileInfo: info}
		f.Backend, _ = r.getBackend(backendName)
		res = append(res, f)
	}

	return res, rows.Err()
}

// Get is like Open, except the FileStream is not populated (useful for HEAD requests)
func (r *FileRepo) Get(name string, body bool) (*backend.File, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}

	var info backend.FileInfo
	var backendName string
	var externalID *string
	var sha512 []byte
	if err := tx.QueryRow(
		"SELECT file_id, file_oid, external_id, backend, name, type, size, sha512, updated_at FROM files WHERE name = $1 AND deleted_at IS NULL",
		name,
	).Scan(&info.ID, &info.Oid, &externalID, &backendName, &info.Name, &info.Type, &info.Size, &sha512, &info.ModTime); err != nil {
		if err == pgx.ErrNoRows {
			err = backend.ErrNotFound
		}
		tx.Rollback()
		return nil, err
	}
	if externalID != nil {
		info.ExternalID = *externalID
	}
	info.ETag = base64.StdEncoding.EncodeToString(sha512)
	if !body {
		tx.Rollback()
		return &backend.File{FileInfo: info, FileStream: fakeSizeSeekerFileStream{info.Size}}, nil
	}

	b, err := r.getBackend(backendName)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	stream, err := b.Open(tx, info, true)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return &backend.File{FileInfo: info, FileStream: stream}, nil
}

func (r *FileRepo) Copy(to, from string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}

	info := backend.FileInfo{
		Name: from,
	}
	var backendName string
	var externalID *string
	var sha512, sha512State []byte
	if err := tx.QueryRow(
		"SELECT file_id, file_oid, external_id, backend, type, size, sha512, sha512_state, updated_at FROM files WHERE name = $1 AND deleted_at IS NULL", from,
	).Scan(&info.ID, &info.Oid, &externalID, &backendName, &info.Type, &info.Size, &sha512, &sha512State, &info.ModTime); err != nil {
		tx.Rollback()
		if err == pgx.ErrNoRows {
			return backend.ErrNotFound
		}
		return err
	}
	if externalID != nil {
		info.ExternalID = *externalID
	}
	b, err := r.getBackend(backendName)
	if err != nil {
		tx.Rollback()
		return err
	}

	toInfo := backend.FileInfo{
		Name: to,
		Type: info.Type,
	}
	if err := tx.QueryRow(
		"INSERT INTO files (backend, name, type, size, sha512, sha512_state) VALUES ($1, $2, $3, $4, $5, $6) RETURNING file_id",
		backendName, to, info.Type, info.Size, sha512, sha512State,
	).Scan(&toInfo.ID); err != nil {
		tx.Rollback()
		return err
	}

	if err := b.Copy(tx, toInfo, info); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// Put stores data as name, or appends it when offset is the size of the
// stored object. It reads data with no transaction open (EDGE-33): the
// object is written to a new large object or external object first, recorded
// in uploads so an abandoned one can be removed, and only then a short
// transaction replaces the live row. A slow or stalled client holds no pool
// connection and no row lock, so it neither blocks other writers of name nor
// uses up the pool. A failed upload deletes the object it wrote (EDGE-32).
func (r *FileRepo) Put(name string, data io.Reader, offset int64, typ string) error {
	b := r.defaultBackend
	var prev *liveFile
	if offset > 0 {
		var err error
		prev, err = r.liveFile(name)
		if err == backend.ErrNotFound {
			return fmt.Errorf("blobstore: offset (%d) does not match blob size (0), unable to append", offset)
		} else if err != nil {
			return err
		}
		if offset != prev.Size {
			// TODO: pass error via HTTP response
			return fmt.Errorf("blobstore: offset (%d) does not match blob size (%d), unable to append", offset, prev.Size)
		}
		if b, err = r.getBackend(prev.backend); err != nil {
			return err
		}
		old, err := r.openStored(b, prev.FileInfo)
		if err != nil {
			return err
		}
		defer old.Close()
		data = io.MultiReader(io.LimitReader(old, prev.Size), data)
	}

	u, err := r.startUpload(b, name, typ)
	if err != nil {
		return err
	}
	h := sha512.New().(resumable.Hash)
	sr := newSizeReader(&progressReader{r: data, touch: u.touch})
	if err := b.Put(r.db, u.info, io.TeeReader(sr, h)); err != nil {
		r.discardUpload(u)
		return err
	}
	u.info.Size = sr.Size()
	replaced, err := r.finishUpload(u, h, prev)
	if err != nil {
		if err == errCommitInDoubt {
			r.discardUnlessCommitted(u)
		} else {
			r.discardUpload(u)
		}
		return err
	}
	if replaced != nil {
		r.deleteReplaced(name, replaced)
	}
	return nil
}

// liveFile is the stored object an append extends.
type liveFile struct {
	backend.FileInfo
	backend string
}

func (r *FileRepo) liveFile(name string) (*liveFile, error) {
	f := &liveFile{}
	var externalID *string
	var size *int64
	if err := r.db.QueryRow(
		"SELECT file_id, file_oid, external_id, backend, size FROM files WHERE name = $1 AND deleted_at IS NULL", name,
	).Scan(&f.ID, &f.Oid, &externalID, &f.backend, &size); err != nil {
		if err == pgx.ErrNoRows {
			err = backend.ErrNotFound
		}
		return nil, err
	}
	f.Name = name
	if externalID != nil {
		f.ExternalID = *externalID
	}
	if size != nil {
		f.Size = *size
	}
	return f, nil
}

// openStored reads a stored object without holding a transaction.
func (r *FileRepo) openStored(b backend.Backend, info backend.FileInfo) (io.ReadCloser, error) {
	if b.Name() == "postgres" {
		if info.Oid == nil {
			return nil, backend.ErrNotFound
		}
		return io.NopCloser(backend.NewLargeObjectReader(r.db, *info.Oid, info.Size)), nil
	}
	return b.Open(nil, info, false)
}

// upload is an object being written, recorded in the uploads table until a
// files row names it or it is deleted.
type upload struct {
	id      string
	backend backend.Backend
	info    backend.FileInfo
	touch   func()
}

// uploadTouchInterval is how often a running upload marks itself as alive,
// so SweepUploads leaves it alone.
const uploadTouchInterval = 30 * time.Second

func (r *FileRepo) startUpload(b backend.Backend, name, typ string) (*upload, error) {
	u := &upload{backend: b, info: backend.FileInfo{Name: name, Type: typ}}
	if b.Name() == "postgres" {
		var oid pgx.Oid
		if err := r.db.QueryRow(
			"INSERT INTO uploads (name, backend, file_oid) VALUES ($1, $2, lo_create(0)) RETURNING upload_id, file_oid", name, b.Name(),
		).Scan(&u.id, &oid); err != nil {
			return nil, err
		}
		u.info.Oid = &oid
	} else {
		u.info.ExternalID = random.UUID()
		if err := r.db.QueryRow(
			"INSERT INTO uploads (name, backend, external_id) VALUES ($1, $2, $3) RETURNING upload_id", name, b.Name(), u.info.ExternalID,
		).Scan(&u.id); err != nil {
			return nil, err
		}
	}
	last := time.Now()
	u.touch = func() {
		if time.Since(last) < uploadTouchInterval {
			return
		}
		last = time.Now()
		if err := r.db.Exec("UPDATE uploads SET updated_at = now() WHERE upload_id = $1", u.id); err != nil {
			log.Printf("Error marking upload of %s as alive: %s", name, err)
		}
	}
	return u, nil
}

// progressReader calls touch as the body arrives.
type progressReader struct {
	r     io.Reader
	touch func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.touch()
	}
	return n, err
}

var (
	errCommitInDoubt  = errors.New("blobstore: commit failed")
	errUploadSwept    = errors.New("blobstore: upload was abandoned and removed before it finished")
	errAppendConflict = errors.New("blobstore: the object was replaced while the append was in progress, unable to append")
)

// maxSwapAttempts bounds the retries of the final swap when a writer that does
// not take the swap lock (Copy) inserts the same name at the same moment.
const maxSwapAttempts = 5

// finishUpload makes the uploaded object live in one short transaction: it
// marks the current row of the name deleted, inserts the new row and drops
// the upload record. It returns the external object the new row replaced,
// to delete once the commit is done.
func (r *FileRepo) finishUpload(u *upload, h resumable.Hash, prev *liveFile) (*replacedFile, error) {
	sha512State, _ := h.State()
	sum := h.Sum(nil)
	var externalID *string
	if u.info.ExternalID != "" {
		externalID = &u.info.ExternalID
	}
	for attempt := 1; ; attempt++ {
		tx, err := r.db.Begin()
		if err != nil {
			return nil, err
		}
		// swaps of one name take turns; the lock is held for this short
		// transaction only, never while a body is read
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext($1))", u.info.Name); err != nil {
			tx.Rollback()
			return nil, err
		}
		var replaced *replacedFile
		var old backend.FileInfo
		var oldExternalID *string
		var oldBackend string
		err = tx.QueryRow(
			"UPDATE files SET deleted_at = now() WHERE name = $1 AND deleted_at IS NULL RETURNING file_id, external_id, backend", u.info.Name,
		).Scan(&old.ID, &oldExternalID, &oldBackend)
		switch {
		case err == pgx.ErrNoRows:
			old.ID = ""
		case err != nil:
			tx.Rollback()
			return nil, err
		}
		if prev != nil && old.ID != prev.ID {
			tx.Rollback()
			return nil, errAppendConflict
		}
		if old.ID != "" && oldBackend != "postgres" {
			// the Postgres backend's large object is unlinked by a trigger
			// in this transaction; others are deleted after the commit
			if oldExternalID != nil {
				old.ExternalID = *oldExternalID
			}
			replaced = &replacedFile{info: old, backend: oldBackend}
		}

		err = tx.Exec(
			"INSERT INTO files (name, backend, type, file_oid, external_id, size, sha512, sha512_state) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			u.info.Name, u.backend.Name(), u.info.Type, u.info.Oid, externalID, u.info.Size, sum, sha512State,
		)
		if postgres.IsUniquenessError(err, "") && attempt < maxSwapAttempts {
			// another upload of this name committed after this
			// transaction's UPDATE looked for the live row
			tx.Rollback()
			continue
		} else if err != nil {
			tx.Rollback()
			return nil, err
		}

		tag, err := tx.Tx.Exec("DELETE FROM uploads WHERE upload_id = $1", u.id)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			// SweepUploads took it and deleted its object
			tx.Rollback()
			return nil, errUploadSwept
		}
		if err := tx.Commit(); err != nil {
			log.Printf("Error committing upload of %s: %s", u.info.Name, err)
			return nil, errCommitInDoubt
		}
		return replaced, nil
	}
}

// discardUpload deletes the object of an upload that did not commit, and its
// record.
func (r *FileRepo) discardUpload(u *upload) {
	if _, err := r.discard(u.id, u.backend, u.info, 0); err != nil {
		log.Printf("Error deleting the object of the failed upload of %s, left for the sweep: %s", u.info.Name, err)
	}
}

// discardUnlessCommitted handles a commit whose outcome is unknown: the
// upload record is gone only if the commit went through.
func (r *FileRepo) discardUnlessCommitted(u *upload) {
	var pending bool
	if err := r.db.QueryRow("SELECT EXISTS (SELECT 1 FROM uploads WHERE upload_id = $1)", u.id).Scan(&pending); err != nil {
		log.Printf("Error checking the upload of %s after a failed commit, left for the sweep: %s", u.info.Name, err)
		return
	}
	if pending {
		r.discardUpload(u)
	}
}

// discard removes an upload's record and deletes its object. With
// olderThan > 0 (the sweep) it does so only if the upload made no progress
// for that long, and reports whether it did. The record is removed first, so
// an upload that is still running can no longer commit the object; if the
// object of an external backend cannot be deleted the record is put back
// for a later sweep.
func (r *FileRepo) discard(id string, b backend.Backend, info backend.FileInfo, olderThan time.Duration) (bool, error) {
	cond := "true"
	args := []interface{}{id}
	if olderThan > 0 {
		cond = "updated_at < now() - make_interval(secs => $2)"
		args = append(args, olderThan.Seconds())
	}
	if b.Name() == "postgres" {
		// the record and the large object go in one statement
		var claimed, unlinked int64
		err := r.db.QueryRow(`WITH d AS (DELETE FROM uploads WHERE upload_id = $1 AND `+cond+` RETURNING file_oid),
			u AS (SELECT lo_unlink(d.file_oid) FROM d WHERE d.file_oid IS NOT NULL AND EXISTS (SELECT 1 FROM pg_largeobject_metadata m WHERE m.oid = d.file_oid))
			SELECT (SELECT count(*) FROM d), (SELECT count(*) FROM u)`, args...).Scan(&claimed, &unlinked)
		return claimed > 0, err
	}
	tag, err := r.db.ConnPool.Exec("DELETE FROM uploads WHERE upload_id = $1 AND "+cond, args...)
	if err != nil {
		return false, err
	}
	if olderThan > 0 && tag.RowsAffected() == 0 {
		// it made progress since it was listed, or another sweep took it
		return false, nil
	}
	if err := b.Delete(nil, info); err != nil {
		if e := r.db.Exec(
			"INSERT INTO uploads (upload_id, name, backend, external_id) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
			id, info.Name, b.Name(), info.ExternalID,
		); e != nil {
			log.Printf("Error recording the object %s of %s for a later sweep, it is no longer tracked: %s", info.ExternalID, info.Name, e)
		}
		return false, err
	}
	return true, nil
}

// PendingUploads counts the uploads in progress or abandoned.
func (r *FileRepo) PendingUploads() (int, error) {
	var n int
	err := r.db.QueryRow("SELECT count(*) FROM uploads").Scan(&n)
	return n, err
}

// SweepUploads deletes the objects of uploads that made no progress for
// olderThan: their client went away without the blobstore noticing, or the
// instance that served them stopped. It returns how many it removed. An
// upload whose backend is not configured is left alone.
func (r *FileRepo) SweepUploads(olderThan time.Duration) (int, error) {
	rows, err := r.db.Query(
		"SELECT upload_id, name, backend, file_oid, external_id FROM uploads WHERE updated_at < now() - make_interval(secs => $1)", olderThan.Seconds(),
	)
	if err != nil {
		return 0, err
	}
	type stale struct {
		id, backend string
		info        backend.FileInfo
	}
	var list []stale
	for rows.Next() {
		var s stale
		var externalID *string
		if err := rows.Scan(&s.id, &s.info.Name, &s.backend, &s.info.Oid, &externalID); err != nil {
			rows.Close()
			return 0, err
		}
		if externalID != nil {
			s.info.ExternalID = *externalID
		}
		list = append(list, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var n int
	var errs []error
	for _, s := range list {
		b, err := r.getBackend(s.backend)
		if err != nil {
			log.Printf("Not removing abandoned upload of %s: %s", s.info.Name, err)
			continue
		}
		claimed, err := r.discard(s.id, b, s.info, olderThan)
		if err != nil {
			errs = append(errs, fmt.Errorf("removing abandoned upload of %s: %w", s.info.Name, err))
			continue
		}
		if !claimed {
			continue
		}
		log.Printf("Removed abandoned upload of %s (%s)", s.info.Name, s.backend)
		n++
	}
	return n, errors.Join(errs...)
}

// replacedFile is the external backend object of a row that an overwrite
// marked deleted.
type replacedFile struct {
	info    backend.FileInfo
	backend string
}

// deleteReplaced removes the object an overwrite replaced. It runs only after
// the overwrite committed. A failure is logged and the object stays listed
// (its row has deleted_at set) for `flynn-blobstore cleanup`.
func (r *FileRepo) deleteReplaced(name string, f *replacedFile) {
	b, err := r.getBackend(f.backend)
	if err == nil {
		err = b.Delete(nil, f.info)
	}
	if err != nil {
		log.Printf("Error deleting replaced %s (%s) from backend %s, left for cleanup: %s", f.info.ExternalID, name, f.backend, err)
	}
}

func (r *FileRepo) Delete(name string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	// use a regular expression so that either a file with the name is
	// deleted, or any file prefixed with "{name}/" is deleted (so in other
	// words, mimic either deleting a file or recursively deleting a
	// directory)
	rows, err := tx.Query(
		"UPDATE files SET deleted_at = now() WHERE name ~ ('^' || $1 || '(/.*)?$') AND deleted_at IS NULL RETURNING file_id, external_id, backend, name", name,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	backendFiles := make(map[string][]backend.FileInfo)
	for rows.Next() {
		var info backend.FileInfo
		var backendName string
		var externalID *string
		if err := rows.Scan(&info.ID, &externalID, &backendName, &info.Name); err != nil {
			rows.Close()
			tx.Rollback()
			return err
		}
		if externalID != nil {
			info.ExternalID = *externalID
		}
		backendFiles[backendName] = append(backendFiles[backendName], info)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// The rows are already marked deleted, so an object that is not removed
	// here stays listed for `flynn-blobstore cleanup`. Every failure is
	// returned, and a backend that is no longer configured is skipped
	// instead of stopping the deletes on the others.
	var errs []error
	for name, files := range backendFiles {
		if name == "postgres" {
			// no need to call delete, it is done automatically by a trigger
			continue
		}
		b, err := r.getBackend(name)
		if err != nil {
			log.Printf("Not deleting %d file(s) from backend %s, left for cleanup: %s", len(files), name, err)
			errs = append(errs, err)
			continue
		}
		for _, f := range files {
			if err := b.Delete(nil, f); err != nil {
				log.Printf("Error deleting %s (%s) from backend %s, left for cleanup: %s", f.ExternalID, f.Name, name, err)
				errs = append(errs, fmt.Errorf("deleting %s from backend %s: %w", f.Name, name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// PurgeDeleted removes the row of a deleted file once `flynn-blobstore
// cleanup` has deleted its backend object, so the next run does not list it
// again. A live row is never removed.
func (r *FileRepo) PurgeDeleted(id string) error {
	return r.db.Exec("DELETE FROM files WHERE file_id = $1 AND deleted_at IS NOT NULL", id)
}

// SetBackend points the row id at its copy info in backend b (migrate).
func (f *FileRepo) SetBackend(tx *postgres.DBTx, id string, b backend.Backend, info backend.FileInfo) error {
	if info.Oid != nil {
		return tx.Exec("UPDATE files SET backend = $2, file_oid = $3, external_id = NULL WHERE file_id = $1", id, b.Name(), *info.Oid)
	}
	return tx.Exec("UPDATE files SET backend = $2, external_id = $3 WHERE file_id = $1", id, b.Name(), info.ExternalID)
}

type sizeReader struct {
	size int64
	r    io.Reader
}

func (sr *sizeReader) Read(p []byte) (int, error) {
	n, err := sr.r.Read(p)
	sr.size = sr.size + int64(n)
	return n, err
}

func (sr *sizeReader) Size() int64 {
	return sr.size
}

func newSizeReader(r io.Reader) *sizeReader {
	return &sizeReader{r: r}
}

// HACK: work around ServeContent length detection, remove when fixed
type fakeSizeSeekerFileStream struct {
	size int64
}

func (fakeSizeSeekerFileStream) Close() error             { return nil }
func (fakeSizeSeekerFileStream) Read([]byte) (int, error) { return 0, io.EOF }
func (f fakeSizeSeekerFileStream) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekEnd {
		return f.size, nil
	}
	return 0, nil
}

// AppUsage is the size of the live objects stored under app's names (see
// appForPath in the blobstore server): /slugs/apps/<app>/,
// /tarreceive/apps/<app>/, /<app>-cache.tgz, /<app>-docker-cache.tgz and
// /repos/<app>.tar. exclude is left out (the object a PUT replaces). An
// upload still in progress has no size yet and is not counted.
func (r *FileRepo) AppUsage(app, exclude string) (int64, error) {
	var used int64
	err := r.db.QueryRow(
		`SELECT COALESCE(SUM(size), 0)::bigint FROM files WHERE deleted_at IS NULL AND name <> $1 AND (
			left(name, length($2)) = $2 OR left(name, length($3)) = $3 OR name IN ($4, $5, $6))`,
		exclude,
		"/slugs/apps/"+app+"/", "/tarreceive/apps/"+app+"/",
		"/"+app+"-cache.tgz", "/"+app+"-docker-cache.tgz", "/repos/"+app+".tar",
	).Scan(&used)
	return used, err
}

// DB is the repo's database.
func (r *FileRepo) DB() *postgres.DB { return r.db }

// layerNamePattern matches the layer objects tarreceive stores: the shared
// /tarreceive/layers/<id>.{squashfs,json} and, since EDGE-35, each app's
// /tarreceive/apps/<app>/layers/<id>.{squashfs,json}.
const layerNamePattern = `^/tarreceive/(layers|apps/[^/]+/layers)/[^/]+\.(squashfs|json)$`

// LayerObject is a live tarreceive layer object (its squashfs or config).
type LayerObject struct {
	Name string
	Size int64
	// Age is how long ago the object was stored (by the database clock).
	Age time.Duration
	// Uploading is set when an upload of the layer's squashfs or config
	// is in progress (or abandoned and not yet swept).
	Uploading bool
}

// ListLayerObjects lists the live tarreceive layer objects, for the layer
// GC (`flynn-blobstore cleanup-layers`).
func (r *FileRepo) ListLayerObjects() ([]LayerObject, error) {
	rows, err := r.db.Query(`
SELECT f.name, COALESCE(f.size, 0)::bigint, EXTRACT(EPOCH FROM now() - f.created_at)::float8,
  EXISTS (SELECT 1 FROM uploads u WHERE regexp_replace(u.name, '\.(squashfs|json)$', '') = regexp_replace(f.name, '\.(squashfs|json)$', ''))
FROM files f WHERE f.deleted_at IS NULL AND f.name ~ $1 ORDER BY f.name`, layerNamePattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []LayerObject
	for rows.Next() {
		var o LayerObject
		var age float64
		if err := rows.Scan(&o.Name, &o.Size, &age, &o.Uploading); err != nil {
			return nil, err
		}
		o.Age = time.Duration(age * float64(time.Second))
		res = append(res, o)
	}
	return res, rows.Err()
}

// DeleteIdleLayer deletes the live objects named names (one layer's
// squashfs and config) only if none of them was stored within grace and no
// upload of any of them is in progress, checked under the same per-name locks
// an upload takes to make its object live, so an object re-put (a tarreceive
// reuse) after the GC listed it is kept. It reports whether it deleted the
// layer and how many bytes it freed. Objects in an external backend are
// deleted after the commit; one that fails stays listed for
// `flynn-blobstore cleanup`.
func (r *FileRepo) DeleteIdleLayer(names []string, grace time.Duration) (bool, int64, error) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	tx, err := r.db.Begin()
	if err != nil {
		return false, 0, err
	}
	for _, name := range sorted {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext($1))", name); err != nil {
			tx.Rollback()
			return false, 0, err
		}
	}
	var busy bool
	if err := tx.QueryRow(`SELECT
  EXISTS (SELECT 1 FROM files WHERE name = ANY($1) AND deleted_at IS NULL AND created_at > now() - make_interval(secs => $2))
  OR EXISTS (SELECT 1 FROM uploads WHERE name = ANY($1))`, sorted, grace.Seconds()).Scan(&busy); err != nil {
		tx.Rollback()
		return false, 0, err
	}
	if busy {
		tx.Rollback()
		return false, 0, nil
	}
	rows, err := tx.Query(
		"UPDATE files SET deleted_at = now() WHERE name = ANY($1) AND deleted_at IS NULL RETURNING file_id, external_id, backend, name, COALESCE(size, 0)::bigint", sorted,
	)
	if err != nil {
		tx.Rollback()
		return false, 0, err
	}
	var freed int64
	var marked int
	external := make(map[string][]backend.FileInfo)
	for rows.Next() {
		marked++
		var info backend.FileInfo
		var backendName string
		var externalID *string
		var size int64
		if err := rows.Scan(&info.ID, &externalID, &backendName, &info.Name, &size); err != nil {
			rows.Close()
			tx.Rollback()
			return false, 0, err
		}
		if externalID != nil {
			info.ExternalID = *externalID
		}
		freed += size
		if backendName != "postgres" {
			// the Postgres large object is unlinked by the trigger
			external[backendName] = append(external[backendName], info)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return false, 0, err
	}
	if marked == 0 {
		// another sweep (every server instance runs one) got it first
		tx.Rollback()
		return false, 0, nil
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	var errs []error
	for name, files := range external {
		b, err := r.getBackend(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("layer objects left for cleanup in backend %s: %w", name, err))
			continue
		}
		for _, f := range files {
			if err := b.Delete(nil, f); err != nil {
				errs = append(errs, fmt.Errorf("deleting %s from backend %s, left for cleanup: %w", f.Name, name, err))
			}
		}
	}
	return true, freed, errors.Join(errs...)
}

package main

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"

	docopt "github.com/flynn/go-docopt"
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

func init() {
	register("migrate", runMigrate, `
usage: flynn-blobstore migrate [-c <concurrency>] [--delete] [-p <prefix>]

Move file blobs from default backend to a different backend.

Options:
     -c, --concurrency=<concurrency>  number of parallel file moves to run at a time. [default: 4]
     --delete                         enable deletion of files from source backend. [default: false]   
     -p, --prefix=<prefix>            only migrate files with a name that starts with this prefix. [default: ]
`)
}

// migrateRepo is the part of data.FileRepo that migrate uses.
type migrateRepo interface {
	DefaultBackend() backend.Backend
	ListFilesExcludingDefaultBackend(prefix string) ([]data.BackendFile, error)
	SetBackend(tx *postgres.DBTx, id string, dst backend.Backend, info backend.FileInfo) error
}

// migrateOpen connects to the database and builds the file repo. It is a
// variable so tests can inject failures.
var migrateOpen = func() (*postgres.DB, migrateRepo, error) {
	db := postgres.Wait(nil, nil)
	repo, err := data.NewFileRepoFromEnv(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, repo, nil
}

func runMigrate(args *docopt.Args) error {
	deleteFiles := args.Bool["--delete"]
	concurrency, err := strconv.Atoi(args.String["--concurrency"])
	if err != nil {
		return err
	}
	if concurrency < 1 {
		concurrency = 4
	}
	prefix := args.String["--prefix"]

	db, repo, err := migrateOpen()
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
	}

	files, err := repo.ListFilesExcludingDefaultBackend(prefix)
	if err != nil {
		return fmt.Errorf("listing files to migrate: %s", err)
	}

	var wg sync.WaitGroup
	tokens := make(chan struct{}, concurrency)
	var errorCount int64

	dest := repo.DefaultBackend().Name()
	for i, f := range files {
		log.Printf("[%d/%d] Moving %s (%s, %d bytes) from %s to %s", i+1, len(files), f.FileInfo.Name, f.ID, f.Size, f.Backend.Name(), dest)
		tokens <- struct{}{}
		wg.Add(1)
		go func(f data.BackendFile) {
			if err := moveFile(db, repo, f, deleteFiles); err != nil {
				log.Printf("Error moving %s (%s): %s", f.FileInfo.Name, f.ID, err)
				atomic.AddInt64(&errorCount, 1)
			}
			<-tokens
			wg.Done()
		}(f)
	}

	wg.Wait()
	if errorCount > 0 {
		return fmt.Errorf("Finished with %d errors", errorCount)
	}

	log.Printf("Finished with no errors.")
	return nil
}

func moveFile(db *postgres.DB, repo migrateRepo, f data.BackendFile, delete bool) error {
	b := repo.DefaultBackend()
	// the copy is a new object; the row points at it only once committed
	dst := f.FileInfo
	dst.Oid, dst.ExternalID = nil, ""
	if b.Name() == "postgres" {
		var oid pgx.Oid
		if err := db.QueryRow("SELECT lo_create(0)").Scan(&oid); err != nil {
			return err
		}
		dst.Oid = &oid
	} else {
		dst.ExternalID = random.UUID()
	}
	// discard deletes the copy when the move fails before its commit
	discard := func() {
		var err error
		if dst.Oid != nil {
			err = db.Exec("SELECT lo_unlink($1)", *dst.Oid)
		} else {
			err = b.Delete(nil, dst)
		}
		if err != nil {
			log.Printf("Error deleting the partial copy of %s (%s) from %s: %s", f.FileInfo.Name, f.ID, b.Name(), err)
		}
	}

	// Reading a large object needs a transaction; the other backends are
	// read through a URL. The Postgres backend's Put takes connections of
	// its own, so it never runs while this one waits on them.
	var tx *postgres.DBTx
	var err error
	if f.Backend.Name() == "postgres" {
		if tx, err = db.Begin(); err != nil {
			discard()
			return err
		}
	}
	stream, err := f.Backend.Open(tx, f.FileInfo, false)
	if err != nil {
		if tx != nil {
			tx.Rollback()
		}
		discard()
		return err
	}
	err = b.Put(db, dst, stream)
	stream.Close()
	if err != nil {
		if tx != nil {
			tx.Rollback()
		}
		discard()
		return err
	}
	if tx == nil {
		if tx, err = db.Begin(); err != nil {
			discard()
			return err
		}
	}
	if err := repo.SetBackend(tx, f.ID, b, dst); err != nil {
		tx.Rollback()
		discard()
		return err
	}
	if err := commitMove(tx); err != nil {
		// the commit may have gone through, so the copy is kept
		return err
	}
	if delete {
		// Delete from the source only after the new location is committed, so
		// a failed commit never leaves the row pointing at a deleted blob.
		if err := deleteSource(db, f); err != nil {
			// print but don't return error if deletion of old file fails, we don't want to lose files
			log.Printf("Error deleting %s (%s) from %s: %s", f.FileInfo.Name, f.ID, f.Backend.Name(), err)
		}
	}
	return nil
}

// commitMove is a variable so tests can inject a commit failure.
var commitMove = func(tx *postgres.DBTx) error { return tx.Commit() }

func deleteSource(db *postgres.DB, f data.BackendFile) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := f.Backend.Delete(tx, f.FileInfo); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

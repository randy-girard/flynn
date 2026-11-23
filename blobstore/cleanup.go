package main

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"

	docopt "github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/postgres"
)

func init() {
	register("cleanup", runCleanup, `
usage: flynn-blobstore cleanup [-c <concurrency>]

Delete the backend objects of files that were deleted or overwritten but are
still listed (rows with deleted_at set on a non-postgres backend). Exits
non-zero when any object could not be deleted, including objects whose backend
is not configured in this release.

Options:
     -c, --concurrency=<concurrency>  number of parallel file deletions to run at a time. [default: 4]
`)
}

// cleanupRepo is the part of data.FileRepo that cleanup uses.
type cleanupRepo interface {
	ListDeletedFilesForCleanup() ([]data.BackendFile, error)
	PurgeDeleted(id string) error
}

// cleanupOpen connects to the database and builds the file repo. It is a
// variable so tests can inject a fake repo.
var cleanupOpen = func() (*postgres.DB, cleanupRepo, error) {
	db := postgres.Wait(nil, nil)
	repo, err := data.NewFileRepoFromEnv(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, repo, nil
}

func runCleanup(args *docopt.Args) error {
	concurrency, err := strconv.Atoi(args.String["--concurrency"])
	if err != nil {
		return err
	}
	if concurrency < 1 {
		concurrency = 4
	}

	db, repo, err := cleanupOpen()
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
	}

	files, err := repo.ListDeletedFilesForCleanup()
	if err != nil {
		return fmt.Errorf("listing deleted files: %s", err)
	}

	var wg sync.WaitGroup
	tokens := make(chan struct{}, concurrency)
	var failed int64
	for i, f := range files {
		if f.Backend == nil {
			// the object is still in a backend this release cannot reach
			log.Printf("[%d/%d] Skipping %s (%s) because backend is not configured", i+1, len(files), f.FileInfo.Name, f.ID)
			atomic.AddInt64(&failed, 1)
			continue
		}
		tokens <- struct{}{}
		wg.Add(1)
		go func(f data.BackendFile) {
			defer wg.Done()
			defer func() { <-tokens }()
			if err := f.Backend.Delete(nil, f.FileInfo); err != nil {
				log.Printf("[%d/%d] Error deleting %s (%s) from backend %s: %s", i+1, len(files), f.FileInfo.Name, f.ID, f.Backend.Name(), err)
				atomic.AddInt64(&failed, 1)
				return
			}
			// the object is gone; drop its row so the next run skips it
			if err := repo.PurgeDeleted(f.ID); err != nil {
				log.Printf("[%d/%d] Deleted %s (%s) from backend %s but could not remove its row: %s", i+1, len(files), f.FileInfo.Name, f.ID, f.Backend.Name(), err)
				atomic.AddInt64(&failed, 1)
				return
			}
			log.Printf("[%d/%d] Successfully deleted %s (%s) from backend %s", i+1, len(files), f.FileInfo.Name, f.ID, f.Backend.Name())
		}(f)
	}

	wg.Wait()
	if failed > 0 {
		return fmt.Errorf("%d of %d files were not deleted from their backend", failed, len(files))
	}

	log.Printf("Done, deleted %d files.", len(files))
	return nil
}

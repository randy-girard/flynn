package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"time"

	docopt "github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// NEW-48: before NEW-05 and EDGE-35, slugbuilder stored slugs in the shared
// /slugs/layers/ and /slugs/images/ directories and tarreceive stored image
// manifests in /tarreceive/images/. Those objects count toward no app's
// APP_QUOTA, and once the release that used one is gone nothing deletes it.
// They are not moved under the per-app prefixes (a running release's
// artifact names them by URL); instead the legacy GC deletes an object when
//
//   - no live artifact in the controller names it, as a layer or as its URI,
//   - it is older than the grace period (default 7 days), and
//   - no upload of it is in progress,
//
// checked again under the upload locks when it is deleted. It runs only as
// `flynn-blobstore cleanup-legacy`, which is a dry run unless --delete is
// given. The shared /tarreceive/layers/ store is the layer GC's.

const defaultLegacyGCGrace = 7 * 24 * time.Hour

// legacyRepo is the part of data.FileRepo the legacy GC uses.
type legacyRepo interface {
	ListLegacyObjects() ([]data.LayerObject, error)
	DeleteIdleLayer(names []string, grace time.Duration) (bool, int64, error)
}

type legacyGC struct {
	repo legacyRepo
	// live returns the blobstore paths every live artifact names (its
	// layers and its URI).
	live   func() (map[string]bool, error)
	grace  time.Duration
	dryRun bool
}

func (g *legacyGC) run() (layerGCResult, error) {
	var res layerGCResult
	if g.grace <= 0 {
		return res, errors.New("the legacy GC grace period must be positive")
	}
	// list the objects first and the live set second, as the layer GC does
	objects, err := g.repo.ListLegacyObjects()
	if err != nil {
		return res, fmt.Errorf("listing legacy objects: %s", err)
	}
	live, err := g.live()
	if err != nil {
		return res, fmt.Errorf("reading the objects artifacts use, deleting nothing: %s", err)
	}
	var errs []error
	for _, o := range objects {
		switch {
		case live[o.Name]:
			res.Referenced++
			continue
		case o.Uploading:
			res.Uploading++
			continue
		case o.Age < g.grace:
			res.Young++
			continue
		}
		if g.dryRun {
			log.Printf("would delete unreferenced legacy object %s (%d bytes)", o.Name, o.Size)
			res.Deleted = append(res.Deleted, o.Name)
			res.DeletedBytes += o.Size
			continue
		}
		deleted, freed, err := g.repo.DeleteIdleLayer([]string{o.Name}, g.grace)
		if err != nil {
			errs = append(errs, fmt.Errorf("deleting %s: %w", o.Name, err))
		}
		if !deleted {
			if err == nil {
				// stored again or uploading since the listing
				res.Young++
			}
			continue
		}
		log.Printf("deleted unreferenced legacy object %s (%d bytes)", o.Name, freed)
		res.Deleted = append(res.Deleted, o.Name)
		res.DeletedBytes += freed
	}
	return res, errors.Join(errs...)
}

// liveArtifactPaths is the blobstore path of every layer the artifacts name
// and of every artifact URI on the blobstore. Like liveLayerPaths it refuses
// an empty artifact list.
func liveArtifactPaths(artifacts []*ct.Artifact) (map[string]bool, error) {
	paths, err := liveLayerPaths(artifacts)
	if err != nil {
		return nil, err
	}
	for _, a := range artifacts {
		if a == nil || a.URI == "" {
			continue
		}
		if u, err := url.Parse(a.URI); err == nil && blobstoreauth.IsBlobstoreURL(u) {
			paths[path.Clean(u.Path)] = true
		}
	}
	return paths, nil
}

func controllerLiveArtifactPaths() (map[string]bool, error) {
	key := os.Getenv("CONTROLLER_KEY")
	if key == "" {
		return nil, errors.New("CONTROLLER_KEY is not set")
	}
	client, err := controller.NewClient("", key)
	if err != nil {
		return nil, err
	}
	artifacts, err := client.ArtifactList()
	if err != nil {
		return nil, err
	}
	return liveArtifactPaths(artifacts)
}

// cleanupLegacyOpen connects g to the database and the controller. It is a
// variable so tests can inject fakes; the returned func closes what it opened.
var cleanupLegacyOpen = func(g *legacyGC) (func(), error) {
	db := postgres.Wait(nil, nil)
	repo, err := data.NewFileRepoFromEnv(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	g.repo = repo
	g.live = controllerLiveArtifactPaths
	return db.Close, nil
}

func runCleanupLegacy(args *docopt.Args) error {
	grace, err := time.ParseDuration(args.String["--grace"])
	if err != nil {
		return fmt.Errorf("error parsing --grace: %s", err)
	}
	if grace <= 0 {
		return fmt.Errorf("--grace must be positive, got %s", args.String["--grace"])
	}
	g := &legacyGC{grace: grace, dryRun: !args.Bool["--delete"]}
	closeFn, err := cleanupLegacyOpen(g)
	if err != nil {
		return err
	}
	defer closeFn()
	res, err := g.run()
	if g.dryRun {
		log.Printf("Dry run, nothing deleted (pass --delete to delete): %s", legacyResultString(res))
	} else {
		log.Printf("Done: deleted %s", legacyResultString(res))
	}
	return err
}

func legacyResultString(r layerGCResult) string {
	return fmt.Sprintf("%d unreferenced legacy objects (%d bytes) past the grace period, kept %d referenced, %d younger than the grace period, %d uploading",
		len(r.Deleted), r.DeletedBytes, r.Referenced, r.Young, r.Uploading)
}

func init() {
	register("cleanup-legacy", runCleanupLegacy, `
usage: flynn-blobstore cleanup-legacy [--delete] [--grace=<duration>]

List, and with --delete delete, the objects in the legacy shared directories
(/slugs/layers/, /slugs/images/ and /tarreceive/images/, written before slugs
and image manifests were stored per app) that no artifact in the controller
names as a layer or as its URI, that are older than the grace period, and that
have no upload in progress. Without --delete nothing is deleted. Objects an
artifact still names are kept where they are. Exits non-zero when the
controller cannot be read (nothing is deleted then) or an object could not be
deleted.

Options:
     --delete                 delete the objects (default: list them only)
     -g, --grace=<duration>   how old an unreferenced object must be [default: 168h]
`)
}

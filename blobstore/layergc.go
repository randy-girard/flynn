package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	docopt "github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// EDGE-35: Docker image layers that tarreceive stored (the shared
// /tarreceive/layers/ store and each app's /tarreceive/apps/<app>/layers/)
// are deleted with their release when no other artifact names them, but a
// layer whose push failed after the upload, or whose artifact never made it
// into a release, is named by nothing and was kept forever. The layer GC
// deletes a layer (its squashfs and its config) when
//
//   - no live artifact in the controller names it (any app, any release,
//     including artifacts in no release),
//   - every one of its objects is older than the grace period (a reuse by
//     tarreceive re-puts the config, which starts the period again), and
//   - no upload of it is in progress.
//
// It runs as `flynn-blobstore cleanup-layers` (with --dry-run), and inside
// the server every LAYER_GC_INTERVAL when an operator turns that on.

// defaultLayerGCGrace is how old an unreferenced layer must be before it is
// deleted. A push uploads its layer seconds to minutes before it registers
// the artifact, so a day leaves a wide margin.
const defaultLayerGCGrace = 24 * time.Hour

// layerRepo is the part of data.FileRepo the layer GC uses.
type layerRepo interface {
	ListLayerObjects() ([]data.LayerObject, error)
	DeleteIdleLayer(names []string, grace time.Duration) (bool, int64, error)
}

type layerGC struct {
	repo layerRepo
	// live returns the blobstore paths of every layer squashfs a live
	// artifact names.
	live   func() (map[string]bool, error)
	grace  time.Duration
	dryRun bool
}

// layerGCResult counts the layers a run looked at, by what it did with them.
type layerGCResult struct {
	// Deleted is the layers deleted (or, in a dry run, that would be),
	// each as its path without the .squashfs / .json suffix.
	Deleted      []string
	DeletedBytes int64
	// Referenced, Young and Uploading are the layers kept because an
	// artifact names them, an object of theirs is younger than the grace
	// period, or an upload of theirs is in progress.
	Referenced, Young, Uploading int
}

func (r layerGCResult) String() string {
	return fmt.Sprintf("%d unreferenced layers (%d bytes) past the grace period, kept %d referenced, %d younger than the grace period, %d uploading",
		len(r.Deleted), r.DeletedBytes, r.Referenced, r.Young, r.Uploading)
}

// layerStem is a layer object name without its .squashfs / .json suffix.
func layerStem(name string) string {
	if s := strings.TrimSuffix(name, ".squashfs"); s != name {
		return s
	}
	return strings.TrimSuffix(name, ".json")
}

func (g *layerGC) run() (layerGCResult, error) {
	var res layerGCResult
	if g.grace <= 0 {
		return res, errors.New("the layer GC grace period must be positive")
	}
	// list the objects first and the live set second: an artifact created
	// after the listing names a layer that is already younger than the
	// grace period or was re-put by its reuse (DeleteIdleLayer checks again)
	objects, err := g.repo.ListLayerObjects()
	if err != nil {
		return res, fmt.Errorf("listing layer objects: %s", err)
	}
	live, err := g.live()
	if err != nil {
		return res, fmt.Errorf("reading the layers artifacts use, deleting nothing: %s", err)
	}

	type layer struct {
		size      int64
		young     bool
		uploading bool
	}
	layers := make(map[string]*layer)
	var stems []string
	for _, o := range objects {
		stem := layerStem(o.Name)
		l, ok := layers[stem]
		if !ok {
			l = &layer{}
			layers[stem] = l
			stems = append(stems, stem)
		}
		l.size += o.Size
		l.young = l.young || o.Age < g.grace
		l.uploading = l.uploading || o.Uploading
	}
	sort.Strings(stems)

	var errs []error
	for _, stem := range stems {
		l := layers[stem]
		switch {
		case live[stem+".squashfs"]:
			res.Referenced++
			continue
		case l.uploading:
			res.Uploading++
			continue
		case l.young:
			res.Young++
			continue
		}
		if g.dryRun {
			log.Printf("would delete unreferenced layer %s (%d bytes)", stem, l.size)
			res.Deleted = append(res.Deleted, stem)
			res.DeletedBytes += l.size
			continue
		}
		deleted, freed, err := g.repo.DeleteIdleLayer([]string{stem + ".squashfs", stem + ".json"}, g.grace)
		if err != nil {
			errs = append(errs, fmt.Errorf("deleting layer %s: %w", stem, err))
		}
		if !deleted {
			if err == nil {
				// stored again, uploading, or deleted by another
				// instance's sweep since the listing
				res.Young++
			}
			continue
		}
		log.Printf("deleted unreferenced layer %s (%d bytes)", stem, freed)
		res.Deleted = append(res.Deleted, stem)
		res.DeletedBytes += freed
	}
	return res, errors.Join(errs...)
}

// liveLayerPaths is the blobstore path of every layer the artifacts name. An
// empty list is refused: a cluster always has artifacts (its own system
// images), so no artifacts at all means the answer is wrong, and sweeping on
// it would delete every layer.
func liveLayerPaths(artifacts []*ct.Artifact) (map[string]bool, error) {
	if len(artifacts) == 0 {
		return nil, errors.New("the controller listed no artifacts")
	}
	paths := make(map[string]bool)
	for _, a := range artifacts {
		if a == nil || a.Type != ct.ArtifactTypeFlynn || len(a.RawManifest) == 0 {
			continue
		}
		for _, rootfs := range a.Manifest().Rootfs {
			if rootfs == nil {
				continue
			}
			for _, layer := range rootfs.Layers {
				if layer == nil {
					continue
				}
				u, err := url.Parse(a.LayerURL(layer))
				if err != nil || !blobstoreauth.IsBlobstoreURL(u) {
					continue
				}
				paths[path.Clean(u.Path)] = true
			}
		}
	}
	return paths, nil
}

// controllerLiveLayers reads the live layer set from the controller with the
// cluster key.
func controllerLiveLayers() (map[string]bool, error) {
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
	return liveLayerPaths(artifacts)
}

// layerGCFromEnv reads LAYER_GC_INTERVAL (how often the server runs the
// layer GC; unset or 0 is off) and LAYER_GC_GRACE (default 24h).
func layerGCFromEnv() (interval, grace time.Duration, err error) {
	if interval, err = parseIdleTimeout(os.Getenv("LAYER_GC_INTERVAL"), 0); err != nil {
		return 0, 0, fmt.Errorf("error parsing LAYER_GC_INTERVAL: %s", err)
	}
	grace = defaultLayerGCGrace
	if s := strings.TrimSpace(os.Getenv("LAYER_GC_GRACE")); s != "" {
		if grace, err = time.ParseDuration(s); err != nil {
			return 0, 0, fmt.Errorf("error parsing LAYER_GC_GRACE: %s", err)
		}
		if grace <= 0 {
			return 0, 0, fmt.Errorf("LAYER_GC_GRACE must be positive, got %s", s)
		}
	}
	return interval, grace, nil
}

// sweepLayers runs the layer GC every interval. Every instance sweeps; a
// layer is deleted by one of them.
func sweepLayers(repo *data.FileRepo, interval, grace time.Duration) {
	g := &layerGC{repo: repo, live: controllerLiveLayers, grace: grace}
	for {
		time.Sleep(interval)
		res, err := g.run()
		if err != nil {
			log.Printf("Layer GC: %s; %s", res, err)
		} else if len(res.Deleted) > 0 {
			log.Printf("Layer GC: %s", res)
		}
	}
}

// cleanupLayersOpen connects g to the database and the controller. It is a
// variable so tests can inject fakes; the returned func closes what it opened.
var cleanupLayersOpen = func(g *layerGC) (func(), error) {
	db := postgres.Wait(nil, nil)
	repo, err := data.NewFileRepoFromEnv(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	g.repo = repo
	g.live = controllerLiveLayers
	return db.Close, nil
}

func runCleanupLayers(args *docopt.Args) error {
	grace, err := time.ParseDuration(args.String["--grace"])
	if err != nil {
		return fmt.Errorf("error parsing --grace: %s", err)
	}
	if grace <= 0 {
		return fmt.Errorf("--grace must be positive, got %s", args.String["--grace"])
	}
	g := &layerGC{grace: grace, dryRun: args.Bool["--dry-run"]}
	closeFn, err := cleanupLayersOpen(g)
	if err != nil {
		return err
	}
	defer closeFn()
	res, err := g.run()
	if g.dryRun {
		log.Printf("Dry run, nothing deleted: %s", res)
	} else {
		log.Printf("Done: deleted %s", res)
	}
	return err
}

func init() {
	register("cleanup-layers", runCleanupLayers, `
usage: flynn-blobstore cleanup-layers [--dry-run] [--grace=<duration>]

Delete the Docker image layers tarreceive stored (/tarreceive/layers/ and
/tarreceive/apps/<app>/layers/) that no artifact in the controller names, that
are older than the grace period, and that have no upload in progress. A layer
tarreceive reuses is stored again, which starts its grace period over. Exits
non-zero when the controller cannot be read (nothing is deleted then) or a
layer could not be deleted.

Options:
     -n, --dry-run            list the layers that would be deleted, delete nothing
     -g, --grace=<duration>   how old an unreferenced layer must be [default: 24h]
`)
}

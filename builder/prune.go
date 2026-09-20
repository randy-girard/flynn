package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
)

var cmdPrune = Command{
	Run: runPrune,
	Usage: `
usage: flynn-builder prune [options]

options:
  --max-age=<duration>  evict layers unused for longer than this [default: 168h]
  --images=<path>       images.json whose layers are always kept [default: build/images.json]
  --dir=<path>          layer cache directory [default: /var/lib/flynn/layer-cache]
  -n, --dry-run         print what would be removed without removing it

Evict stale entries from the content-addressed layer cache.

build.sh prep preserves /var/lib/flynn/layer-cache across builds so unchanged
layers are reused instead of rebuilt. Each rebuilt Go layer adds a new squashfs,
so the cache is pruned after every apps build: a layer is removed when it is not
referenced by images.json and neither its squashfs nor its sidecar JSON has been
used (mtime; flynn-builder touches a layer on every cache hit) within --max-age.
`[1:],
}

func runPrune(args *docopt.Args) error {
	maxAge, err := time.ParseDuration(args.String["--max-age"])
	if err != nil || maxAge <= 0 {
		return fmt.Errorf("invalid --max-age %q", args.String["--max-age"])
	}
	referenced, err := referencedLayerIDs(args.String["--images"])
	if err != nil {
		return err
	}
	removed, kept, err := pruneLayerCache(args.String["--dir"], referenced, time.Now().Add(-maxAge), args.Bool["--dry-run"])
	if err != nil {
		return err
	}
	verb := "removed"
	if args.Bool["--dry-run"] {
		verb = "would remove"
	}
	for _, id := range removed {
		fmt.Printf("%s %s\n", verb, id)
	}
	fmt.Printf("layer cache: %s %d, kept %d (referenced %d, max-age %s)\n", verb, len(removed), kept, len(referenced), maxAge)
	return nil
}

// referencedLayerIDs returns every layer ID named by the artifacts in an
// images.json. A missing file references nothing (so only age applies).
func referencedLayerIDs(path string) (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	artifacts, err := loadArtifacts(path)
	if err != nil {
		return nil, err
	}
	for _, a := range artifacts {
		addArtifactLayerIDs(ids, a)
	}
	return ids, nil
}

func addArtifactLayerIDs(ids map[string]struct{}, a *ct.Artifact) {
	if a == nil {
		return
	}
	m := a.Manifest()
	if m == nil {
		return
	}
	for _, rootfs := range m.Rootfs {
		for _, l := range rootfs.Layers {
			if l != nil && l.ID != "" {
				ids[l.ID] = struct{}{}
			}
		}
	}
}

// pruneLayerCache removes <id>.squashfs / <id>.json pairs from dir that are
// not in referenced and whose newest file is older than cutoff. It returns
// the removed IDs (sorted) and the number of IDs kept.
func pruneLayerCache(dir string, referenced map[string]struct{}, cutoff time.Time, dryRun bool) ([]string, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	newest := make(map[string]time.Time)
	files := make(map[string][]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var id string
		switch {
		case strings.HasSuffix(name, ".squashfs"):
			id = strings.TrimSuffix(name, ".squashfs")
		case strings.HasSuffix(name, ".json"):
			id = strings.TrimSuffix(name, ".json")
		default:
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, 0, err
		}
		files[id] = append(files[id], filepath.Join(dir, name))
		if info.ModTime().After(newest[id]) {
			newest[id] = info.ModTime()
		}
	}

	var removed []string
	kept := 0
	for id, paths := range files {
		if _, ok := referenced[id]; ok || !newest[id].Before(cutoff) {
			kept++
			continue
		}
		if !dryRun {
			for _, p := range paths {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return nil, 0, err
				}
			}
		}
		removed = append(removed, id)
	}
	sort.Strings(removed)
	return removed, kept, nil
}

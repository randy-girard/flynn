package app_garbage_collection

import (
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

// BlobstoreAppURI is the blobstore URI for the app image/slug, skipping
// slugrunner and other system images.
func BlobstoreAppURI(rel *ct.Release, arts map[string]*ct.Artifact) (string, bool) {
	if rel == nil {
		return "", false
	}
	for _, id := range rel.ArtifactIDs {
		a := arts[id]
		if a == nil {
			continue
		}
		if a.Meta["flynn.system-image"] == "true" || a.IsSlugrunner() {
			continue
		}
		if a.Blobstore() {
			return a.URI, true
		}
	}
	return "", false
}

// SelectReapReleaseIDs returns inactive blob releases beyond `keep` distinct
// URIs (newest first) and any that are older than maxAge. Current/active
// formations are never selected. already-reaped rows are skipped.
func SelectReapReleaseIDs(
	releases []*ct.Release,
	formations []*ct.Formation,
	keep int,
	maxAge time.Duration,
	now time.Time,
	blobURI func(*ct.Release) (string, bool),
) []string {
	if keep < 0 {
		keep = 0
	}
	active := make(map[string]struct{}, len(formations))
	for _, f := range formations {
		if f == nil {
			continue
		}
		for _, n := range f.Processes {
			if n > 0 {
				active[f.ReleaseID] = struct{}{}
				break
			}
		}
	}

	seen := make(map[string]struct{}, len(releases))
	reap := make([]string, 0)
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		if _, ok := active[rel.ID]; ok {
			continue
		}
		if rel.Meta[ct.MetaGCBlobReaped] == "true" {
			continue
		}
		uri, ok := blobURI(rel)
		if !ok {
			continue
		}
		tooOld := maxAge > 0 && rel.CreatedAt != nil && now.Sub(*rel.CreatedAt) > maxAge
		if tooOld {
			reap = append(reap, rel.ID)
			continue
		}
		if len(seen) >= keep {
			reap = append(reap, rel.ID)
			continue
		}
		seen[uri] = struct{}{}
	}
	return reap
}

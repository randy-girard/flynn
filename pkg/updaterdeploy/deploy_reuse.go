package updaterdeploy

import (
	ct "github.com/randy-girard/flynn/controller/types"
)

// ReusableUpdateRelease finds a newer app release that already uses the
// update image. Scale-timeout retries must deploy that release again instead
// of CreateRelease: each extra release starts another generation of omni jobs
// (HA controller ended smoke with six schedulers).
func ReusableUpdateRelease(currentID, imageArtifactID string, releases []*ct.Release) *ct.Release {
	if imageArtifactID == "" {
		return nil
	}
	var best *ct.Release
	for _, r := range releases {
		if r == nil || r.ID == "" || r.ID == currentID {
			continue
		}
		if len(r.ArtifactIDs) == 0 || r.ArtifactIDs[0] != imageArtifactID {
			continue
		}
		if best == nil {
			best = r
			continue
		}
		if r.CreatedAt != nil && (best.CreatedAt == nil || r.CreatedAt.After(*best.CreatedAt)) {
			best = r
		}
	}
	return best
}

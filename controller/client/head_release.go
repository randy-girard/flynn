package controller

import (
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

// HeadReleaseID is the newest unfinished deployment's new release, else liveID.
// DeploymentList is newest-first.
func HeadReleaseID(liveID string, deps []*ct.Deployment) string {
	for _, d := range deps {
		if d == nil || d.FinishedAt != nil {
			continue
		}
		if id := strings.TrimSpace(d.NewReleaseID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(liveID)
}

// HeadRelease is the release a new env/config change should fork: a queued or
// running deploy's new release, otherwise the live app release.
func HeadRelease(c Client, appID string) (*ct.Release, error) {
	if c == nil {
		return nil, nil
	}
	list, err := c.DeploymentList(appID)
	if err == nil {
		if id := HeadReleaseID("", list); id != "" {
			if rel, rerr := c.GetRelease(id); rerr == nil {
				return rel, nil
			}
		}
	}
	return c.GetAppRelease(appID)
}

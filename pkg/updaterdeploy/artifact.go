package updaterdeploy

import (
	"fmt"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

// Overridable so CreateArtifactWithRetry tests do not sleep 10s.
var artifactRetryDelay = 10 * time.Second

type artifactCreator interface {
	CreateArtifact(artifact *ct.Artifact) error
}

// CreateArtifactWithRetry creates an image artifact, retrying transient
// controller/blobstore errors. HTTP 401 is not retried: after SEC-028 an
// empty AUTH_KEY will never start succeeding.
func CreateArtifactWithRetry(client artifactCreator, name string, img *ct.Artifact, log log15.Logger) error {
	if img == nil {
		return fmt.Errorf("missing %s image artifact", name)
	}
	if client == nil {
		return fmt.Errorf("create %s image artifact: nil controller client", name)
	}
	for attempt := 1; attempt <= 6; attempt++ {
		if err := client.CreateArtifact(img); err != nil {
			if httpclient.IsUnauthorized(err) {
				return fmt.Errorf("create %s image artifact: controller 401 (set AUTH_KEY/CONTROLLER_KEY or keep a running controller job): %w", name, err)
			}
			if log != nil {
				log.Warn("error creating image artifact, retrying",
					"name", name, "attempt", attempt, "err", err)
			}
			if artifactRetryDelay > 0 {
				time.Sleep(artifactRetryDelay)
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("failed to create %s image artifact after retries", name)
}

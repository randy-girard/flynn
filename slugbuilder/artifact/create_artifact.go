package main

import (
	"fmt"

	ct "github.com/randy-girard/flynn/controller/types"
)

type artifactCreator interface {
	CreateArtifact(*ct.Artifact) error
}

// createArtifact registers the slug artifact with the controller and returns
// any failure. When the app-scoped build token was used and the controller
// rejects it (for example a mismatched signing key after a partial update),
// it retries once with the cluster key client from fallback.
func createArtifact(client artifactCreator, usedToken bool, fallback func() (artifactCreator, error), artifact *ct.Artifact) error {
	err := client.CreateArtifact(artifact)
	if err == nil || !usedToken || fallback == nil {
		return err
	}
	fb, fbErr := fallback()
	if fbErr != nil {
		return fmt.Errorf("%w (fallback credential unavailable: %v)", err, fbErr)
	}
	return fb.CreateArtifact(artifact)
}

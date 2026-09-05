package cli

import "fmt"

// updateRolloutPlan is the cluster-wide work a flynn-host update will do
// after local binaries (if any) are installed.
type updateRolloutPlan struct {
	// UpdateRemotes pushes binaries to other hosts and restarts them rolling.
	UpdateRemotes bool
	// RolloutImages pulls layers on every visible host and deploys system apps.
	RolloutImages bool
}

// decideUpdateRollout encodes the 1-node vs multi-host gating for
// flynn-host update (GitHub and tarball paths share this).
//
// Rules:
//   - --all-nodes always updates remotes (unless --images-only) and rolls out images
//     (unless --skip-images).
//   - Without --all-nodes, a discovered single-host cluster still rolls out images
//     (there are no other peers). Multi-host clusters skip image/system-app rollout
//     until the operator re-runs with --all-nodes.
//   - If host count cannot be discovered, image rollout stays off unless --all-nodes.
func decideUpdateRollout(allNodes, skipImages, imagesOnly bool, hostCount int, hostCountKnown bool) updateRolloutPlan {
	plan := updateRolloutPlan{
		UpdateRemotes: allNodes && !imagesOnly,
	}
	if skipImages {
		return plan
	}
	if allNodes {
		plan.RolloutImages = true
		return plan
	}
	if hostCountKnown && hostCount <= 1 {
		plan.RolloutImages = true
	}
	return plan
}

// validateImagesOnlyFlags rejects --images-only without --all-nodes on multi-host.
func validateImagesOnlyFlags(imagesOnly, allNodes bool, hostCount int, hostCountErr error) error {
	if !imagesOnly || allNodes {
		return nil
	}
	if hostCountErr != nil {
		return fmt.Errorf("--all-nodes is required with --images-only when cluster hosts cannot be discovered: %w", hostCountErr)
	}
	if hostCount > 1 {
		return fmt.Errorf("--images-only requires --all-nodes when the cluster has more than one host")
	}
	return nil
}

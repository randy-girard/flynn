package plugin

import ct "github.com/flynn/flynn/controller/types"

// RestoreImage uses a tarball image when this Flynn build still ships one.
// Plugin appliances keep the backup's blobstore layers when the tarball has none.
func RestoreImage(tarball *ct.Artifact, formation *ct.ExpandedFormation) *ct.Artifact {
	if tarball != nil {
		return tarball
	}
	if formation != nil && len(formation.Artifacts) > 0 {
		return formation.Artifacts[0]
	}
	return nil
}

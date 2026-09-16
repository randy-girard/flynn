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

// RestoreProcesses returns a formation that runs the dump process even when
// the backup recorded desired scale 0 (optional sirenia after upgrade).
func RestoreProcesses(p Installed, formation *ct.ExpandedFormation) map[string]int {
	procs := map[string]int{}
	if formation != nil {
		for k, v := range formation.Processes {
			procs[k] = v
		}
	}
	name := p.BackupProcessHint()
	if name == "" {
		name = p.Name
	}
	if name != "" && procs[name] < 1 {
		procs[name] = 1
	}
	return procs
}

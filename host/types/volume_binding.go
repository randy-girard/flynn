package host

import (
	"fmt"
	"strings"
)

// ValidateVolumes rejects volume bindings that cannot be resolved. An
// empty VolumeID used to pass AddJob and then fail asynchronously with
// "required volume , but that volume does not exist".
func (c ContainerConfig) ValidateVolumes() error {
	for i, v := range c.Volumes {
		if strings.TrimSpace(v.VolumeID) == "" {
			return fmt.Errorf("volume binding %d is missing volume id", i)
		}
	}
	return nil
}

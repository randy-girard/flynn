package dbruntime

import (
	"errors"
	"fmt"
)

// VolumeSize is the data volume size in bytes for a new instance of engine
// on runtime name. requested is the provider config "disk" in bytes, which
// addons:create sends for both published runtimes and raw --disk.
//
// A published runtime gives its own disk when requested is zero or matches
// it. Any other requested value is a custom size and needs
// AllowCustomSizes; the provider config comes from the tenant, so this is
// checked again here and not only in the CLI. Zero means the host default
// (volume.DefaultSize, 20 GiB).
//
// The size is the ZFS refquota set when the volume is created. Existing
// volumes keep their size.
func VolumeSize(cat *Catalog, engine, name string, requested int64) (int64, error) {
	if requested < 0 {
		return 0, errors.New("disk must be a positive byte value")
	}
	if cat == nil {
		if requested > 0 {
			return 0, fmt.Errorf("database runtime catalog unavailable; cannot size a %d byte disk", requested)
		}
		return 0, nil
	}
	if rt, ok := cat.Find(engine, name); ok && rt.Disk > 0 {
		if requested == 0 || requested == rt.Disk {
			return rt.Disk, nil
		}
	}
	if requested == 0 {
		return 0, nil
	}
	if !cat.AllowCustomSizes {
		return 0, ErrCustomSizesDisabled
	}
	return requested, nil
}

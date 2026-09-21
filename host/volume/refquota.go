package volume

import "strconv"

// RefquotaBytes returns the ZFS refquota to apply on create.
// A positive Info.Size overrides the default; nil or zero uses DefaultSize.
func RefquotaBytes(info *Info) int64 {
	if info != nil && info.Size > 0 {
		return info.Size
	}
	return DefaultSize
}

// DataFilesystemProps is the ZFS property map for a new data volume.
// RestoreVolumeState must not apply these: existing datasets are left uncapped.
func DataFilesystemProps(mountpoint string, info *Info) map[string]string {
	return map[string]string{
		"mountpoint": mountpoint,
		"refquota":   strconv.FormatInt(RefquotaBytes(info), 10),
	}
}

// ZFSCreateDataVolumeArgs is the `zfs create` argv for a new data volume.
// Kept as a testable string list so quota wiring can be checked without a zpool.
func ZFSCreateDataVolumeArgs(dataset, mountpoint string, info *Info) []string {
	props := DataFilesystemProps(mountpoint, info)
	return []string{
		"create",
		"-o", "mountpoint=" + props["mountpoint"],
		"-o", "refquota=" + props["refquota"],
		dataset,
	}
}

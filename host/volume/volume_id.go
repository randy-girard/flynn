package volume

import (
	"errors"
	"regexp"
)

// ErrInvalidVolumeID is returned when a caller-supplied volume ID is not UUID-like.
var ErrInvalidVolumeID = errors.New("invalid volume id")

// volumeIDPattern is UUID-like and safe to embed in ZFS dataset/mount paths.
// Production volume IDs are minted with random.UUID() (8-4-4-4-12 hex).
var volumeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidVolumeID reports whether id is a UUID-like identifier. Caller-supplied
// IDs that fail this check can path-escape via filepath.Join (SEC-034).
func ValidVolumeID(id string) bool {
	return volumeIDPattern.MatchString(id)
}

// ValidateCreateVolumeID accepts an empty id (the provider generates a UUID)
// and otherwise requires a UUID-like value.
func ValidateCreateVolumeID(id string) error {
	if id == "" {
		return nil
	}
	if !ValidVolumeID(id) {
		return ErrInvalidVolumeID
	}
	return nil
}

package cleanup

import (
	"os"
	"path/filepath"
	"strings"
)

const tarballExtractPrefix = "flynn-tarball-update-"

// RemoveStaleTarballExtractDirs deletes /tmp/flynn-tarball-update-* directories
// except keepDir (the extract directory for the current update).
func RemoveStaleTarballExtractDirs(keepDir string) error {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), tarballExtractPrefix+"*"))
	if err != nil {
		return err
	}
	keepDir = filepath.Clean(keepDir)
	for _, dir := range matches {
		if filepath.Clean(dir) == keepDir {
			continue
		}
		if !strings.HasPrefix(filepath.Base(dir), tarballExtractPrefix) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
	return nil
}

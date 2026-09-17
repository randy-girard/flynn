package cli

import (
	"os"
	"path/filepath"

	"github.com/flynn/flynn/pkg/ghrelease"
	"github.com/flynn/flynn/pkg/version"
)

func hostUpdateCheckFile() string {
	if os.Getuid() == 0 {
		return "/var/lib/flynn/updatecheck"
	}
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "flynn", "updatecheck")
	}
	return filepath.Join(os.TempDir(), "flynn-updatecheck")
}

// NotifyUpgradeIfAvailable prints a stderr note when GitHub has a newer Flynn
// release than this binary. Failures are ignored; tests set FLYNN_SKIP_UPDATE_CHECK.
func NotifyUpgradeIfAvailable() {
	ghrelease.MaybeNotify(ghrelease.NotifyOptions{
		Writer:         os.Stderr,
		CurrentVersion: version.Release(),
		Product:        "Flynn",
		UpgradeCommand: "sudo flynn-host update",
		CheckFile:      hostUpdateCheckFile(),
	})
}

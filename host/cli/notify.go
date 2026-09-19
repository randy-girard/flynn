package cli

import (
	"os"

	"github.com/randy-girard/flynn/pkg/ghrelease"
	"github.com/randy-girard/flynn/pkg/version"
)

func hostUpdateCheckFile() string {
	return ghrelease.DefaultUpdateCheckCachePath()
}

// NotifyUpgradeIfAvailable prints a stderr note when GitHub has a newer Flynn
// release than this binary. Called on every flynn-host command (including
// daemon start). The GitHub lookup is cached on disk (see
// ghrelease.DefaultUpdateCheckCachePath); a known newer release is printed
// on every command. Failures are ignored; tests set FLYNN_SKIP_UPDATE_CHECK.
func NotifyUpgradeIfAvailable() {
	ghrelease.MaybeNotify(ghrelease.NotifyOptions{
		Writer:         os.Stderr,
		CurrentVersion: version.String(),
		Product:        "Flynn",
		UpgradeCommand: "sudo flynn-host update",
		CheckFile:      hostUpdateCheckFile(),
	})
}

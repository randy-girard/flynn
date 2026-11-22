package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
)

const crossAppCheckUsage = `
usage: flynn-host cross-app:check [--all]

List apps that run or serve a release minted for another app.

Controllers before CORE-18 let a caller with deploy or scale rights on its
own app point it at any release id (POST /apps/<app>/deploy or PUT
/apps/<app>/formations/<release>). That app then ran the other app's image
with its env, and GET /apps/<app>/release returned that env. Current
controllers refuse new cross-app deploys and formations, but rows made
earlier stay until an operator clears them.

The check reads GET /cluster/cross-app-releases (cluster key) once. A
formation row is a formation with at least one process whose release
belongs to another app. A current_release row is an app whose current
release belongs to another app. Formations scaled to zero and deleted
apps are not listed. Rows for system apps are hidden unless --all is set
and never fail the check.

The process exits 1 when a non-system row is found. Clear each row as a
cluster admin:

  formation:        flynn -a <app> scale --release <release> <type>=0 ...
  current_release:  flynn -a <app> release:rollback -y <own-release-id>
                    (pick one from flynn -a <app> release), or push a new
                    build of the app.

Do not clear a current_release row with env:set, release:update or
release:add: those copy the current (other app's) env into a new release
owned by the app. Treat the other app's env as exposed to the app's owner
and rotate its credentials. flynn-host update --all-nodes runs this check
after system apps are deployed and prints a warning; it does not fail.

Options:
	--all  also list system apps
`

func init() {
	Register("cross-app:check", runCrossAppCheck, crossAppCheckUsage)
}

// crossAppAPI is the controller surface cross-app:check reads.
type crossAppAPI interface {
	Get(path string, out interface{}) error
}

const crossAppPath = "/cluster/cross-app-releases"

func runCrossAppCheck(args *docopt.Args) error {
	api, err := hostV1()
	if err != nil {
		return err
	}
	return runCrossAppCheckWith(os.Stdout, api, args.Bool["--all"])
}

func listCrossAppReleases(api crossAppAPI) ([]*ct.CrossAppRelease, error) {
	var rows []*ct.CrossAppRelease
	if err := api.Get(crossAppPath, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func runCrossAppCheckWith(w io.Writer, api crossAppAPI, all bool) error {
	rows, err := listCrossAppReleases(api)
	if err != nil {
		return fmt.Errorf("list cross-app releases: %w", err)
	}
	var shown []*ct.CrossAppRelease
	failing := 0
	for _, row := range rows {
		if !row.System {
			failing++
		}
		if row.System && !all {
			continue
		}
		shown = append(shown, row)
	}
	if len(shown) == 0 {
		fmt.Fprintln(w, "No app runs or serves another app's release.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tAPP\tRELEASE\tRELEASE APP\tPROCESSES\tSYSTEM")
	for _, row := range shown {
		releaseApp := row.ReleaseAppName
		if releaseApp == "" {
			releaseApp = row.ReleaseAppID
		}
		if releaseApp == "" {
			releaseApp = "-"
		}
		procs := formatCrossAppProcesses(row.Processes, "=", ",")
		if procs == "" {
			procs = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\n", row.Kind, row.AppName, row.ReleaseID, releaseApp, procs, row.System)
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To clear (as a cluster admin):")
	for _, row := range shown {
		switch row.Kind {
		case ct.CrossAppKindFormation:
			zero := map[string]int{}
			for typ := range row.Processes {
				zero[typ] = 0
			}
			fmt.Fprintf(w, "  flynn -a %s scale --release %s %s\n", row.AppName, row.ReleaseID, formatCrossAppProcesses(zero, "=", " "))
		case ct.CrossAppKindCurrentRelease:
			fmt.Fprintf(w, "  flynn -a %s release:rollback -y <own-release-id>   # or push a new build; not env:set\n", row.AppName)
		}
	}
	fmt.Fprintln(w, "Treat the other app's release env as exposed to that app's owner.")
	if failing == 0 {
		return nil
	}
	return ErrAlreadyLogged{Err: errors.New("cross-app releases found")}
}

// warnCrossAppReleases is the flynn-host update hook. It prints a warning
// when non-system cross-app rows exist and never fails the update. An older
// controller without the route, or a nil API, prints nothing.
func warnCrossAppReleases(w io.Writer, api crossAppAPI) {
	if api == nil {
		return
	}
	rows, err := listCrossAppReleases(api)
	if err != nil {
		if !productionNotFound(err) {
			fmt.Fprintf(w, "WARNING: could not list cross-app releases: %v\n", err)
		}
		return
	}
	apps := map[string]bool{}
	for _, row := range rows {
		if !row.System {
			apps[row.AppID] = true
		}
	}
	if len(apps) == 0 {
		return
	}
	noun := "apps"
	if len(apps) == 1 {
		noun = "app"
	}
	fmt.Fprintf(w, "WARNING: %d %s run or serve another app's release (made before CORE-18). Run flynn-host cross-app:check to list and clear them.\n", len(apps), noun)
}

func formatCrossAppProcesses(procs map[string]int, eq, sep string) string {
	if len(procs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(procs))
	for k := range procs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s%s%d", k, eq, procs[k]))
	}
	return strings.Join(parts, sep)
}

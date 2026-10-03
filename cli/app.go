package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("apps:create", runCreate, `
usage: flynn apps:create [-r <remote>] [-y] [--owner <owner>] [<name>]

Create an application in Flynn. --owner sets owner_account from a handle
(user or org). Without it, the current context handle is used when one is
set. The cluster key with no owner leaves the app unowned (operator/system).

If a name is not provided, a random name will be generated.
Dashboard paths, Flynn system apps, plugins, planned plugins, and public-site hosts are reserved.

If run from a git repository, a 'flynn' remote will be created or replaced that
allows deploying the application via git.

Options:
	-r, --remote=<remote>  Name of git remote to create, empty string for none. [default: flynn]
	-y, --yes              Skip the confirmation prompt if the git remote already exists.
	--owner=<owner>        Handle that owns the new app (defaults to the current context)

Examples:

	$ flynn apps:create
	Created turkeys-stupefy-perry
	https://turkeys-stupefy-perry.1.localflynn.com
	http://turkeys-stupefy-perry.1.localflynn.com
`)

	register("apps:destroy", runDelete, `
usage: flynn apps:destroy [-y] [-r <remote>]

Delete an app.

If run from a git repository with a 'flynn' remote for the app, it will be
removed.

Options:
	-r, --remote=<remote>  Name of git remote to delete, empty string for none. [default: flynn]
	-y, --yes              Skip the confirmation prompt.

Examples:

	$ flynn -a turkeys-stupefy-perry apps:destroy
	Are you sure you want to delete the app "turkeys-stupefy-perry"? (yes/no): yes
	Deleted turkeys-stupefy-perry
`)
	register("apps", runApps, `
usage: flynn apps [--all]

List apps visible to the current credential.

A user token lists only apps you own or are a collaborator on. The cluster
key and other cluster-admin credentials list user apps and installed
plugins (flynn-plugin meta from flynn-host plugin:install) and omit
bootstrap platform/system apps. Pass --all (cluster-admin) for the full
catalog. flynn-host and other operator tools still request that catalog
themselves.

Options:
	--all  Include platform and system apps (cluster-admin)

Examples:

	$ flynn apps
	ID                                NAME
	a1b2c3d4e5f64789a0b1c2d3e4f50617  myapp

	$ flynn apps --all
	ID                                NAME
	a1b2c3d4e5f64789a0b1c2d3e4f50617  myapp
	9d5be7be873c41b9898032c08aa87597  controller
`)

	register("apps:info", runInfo, `
usage: flynn apps:info

Show information for an app.

Examples:

	$ flynn apps:info
	=== example
	Git URL:  https://git.dev.localflynn.com/example.git
	Web URL:  https://example.dev.localflynn.com
	Web URL:  http://example.dev.localflynn.com
	Build:    running
	Deploy:   running  a6d470d6-9638-4d74-ae71-91c3d9887714  (4 seconds ago)

	$ flynn -a example apps:info
	=== example
	Git URL:  https://git.dev.localflynn.com/example.git
	Web URL:  https://example.dev.localflynn.com
	Web URL:  http://example.dev.localflynn.com
`)
}

func runCreate(args *docopt.Args, client controller.Client) error {
	app := &ct.App{}
	app.Name = args.String["<name>"]
	remote := args.String["--remote"]
	owner := strings.TrimSpace(args.String["--owner"])
	if owner == "" && clusterConf != nil {
		owner = strings.TrimSpace(clusterConf.Context)
	}
	if owner != "" {
		account, err := resolveOwner(client, owner)
		if err != nil {
			return err
		}
		app.OwnerAccount = account
	}

	if inGitRepo() && !args.Bool["--yes"] {
		// Test if remote name exists and prompt user
		update, err := promptReplaceRemote(remote)
		if err != nil {
			return err
		}
		if update == false {
			return nil
		}
	}

	// Create the app
	if err := client.CreateApp(app); err != nil {
		return err
	}

	// Register git remote
	if inGitRepo() && remote != "" {
		exec.Command("git", "remote", "remove", remote).Run()
		exec.Command("git", "remote", "add", "--", remote, gitURL(clusterConf, app.Name)).Run()
	}
	log.Printf("Created %s", app.Name)
	printAppRouteURLs(client, app.ID)
	return nil
}

func printAppRouteURLs(client controller.Client, appID string) {
	if client == nil || appID == "" {
		return
	}
	routes, err := client.AppRouteList(appID)
	if err != nil {
		return
	}
	seen := map[string]struct{}{}
	for _, r := range routes {
		for _, u := range r.PublicURLs(clusterRouteDomain(client)) {
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			log.Println(u)
		}
	}
}

func runDelete(args *docopt.Args, client controller.Client) error {
	appName := mustApp()
	remote := args.String["--remote"]

	if !args.Bool["--yes"] {
		if !promptYesNo(fmt.Sprintf("Are you sure you want to delete the app %q?", appName)) {
			return nil
		}
	}

	res, err := client.DeleteApp(appName)
	if err != nil {
		return err
	}

	if remote != "" {
		if remotes, err := gitRemotes(); err == nil {
			if app, ok := remotes[remote]; ok && app.Name == appName {
				exec.Command("git", "remote", "remove", remote).Run()
			}
		}
	}

	log.Printf("Deleted %s (removed %d routes, deleted %d releases, deprovisioned %d resources)",
		appName, len(res.DeletedRoutes), len(res.DeletedReleases), len(res.DeletedResources))
	return nil
}

func runApps(args *docopt.Args, client controller.Client) error {
	apps, err := listApps(client, args.Bool["--all"])
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	listRec(w, "ID", "NAME")
	for _, a := range apps {
		listRec(w, a.ID, a.Name)
	}
	return nil
}

type appCatalog interface {
	AppList() ([]*ct.App, error)
	AppListVisible() ([]*ct.App, error)
}

func listApps(client appCatalog, all bool) ([]*ct.App, error) {
	if all {
		return client.AppList()
	}
	return client.AppListVisible()
}

func runInfo(_ *docopt.Args, client controller.Client) error {
	appName := mustApp()

	fmt.Println("===", appName)

	w := tabWriter()
	defer w.Flush()

	if release, err := client.GetAppRelease(appName); err == nil || err == controller.ErrNotFound {
		if err == controller.ErrNotFound || release.IsGitDeploy() {
			listRec(w, "Git URL:", gitURL(clusterConf, appName))
		}
	} else {
		return err
	}

	if routes, err := client.AppRouteList(appName); err == nil {
		seen := map[string]struct{}{}
		for _, k := range routes {
			label := "Web URL:"
			if k.Type == "tcp" {
				label = "TCP:"
			}
			for _, u := range k.PublicURLs(clusterRouteDomain(client)) {
				if _, ok := seen[u]; ok {
					continue
				}
				seen[u] = struct{}{}
				listRec(w, label, u)
			}
		}
	}

	if app, err := client.GetApp(appName); err == nil && (app.Building || ct.AppMetaIsBuilding(app.Meta, time.Now())) {
		listRec(w, "Build:", "running")
	}

	if deps, err := client.DeploymentList(appName); err == nil {
		if d := inProgressDeployment(deps); d != nil {
			status := d.Status
			if status == "" {
				status = "running"
			}
			listRec(w, "Deploy:", status, d.ID, "("+humanTime(d.CreatedAt)+")")
		}
	}

	return nil
}

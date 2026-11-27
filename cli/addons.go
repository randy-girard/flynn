package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/resname"
)

const metaAddonAttachments = "flynn-addon-attachments"

func init() {
	register("addons", runAddonsList, `
usage: flynn addons

List add-ons on this app.

NAME is the instance (postgresql-concave-48291) or the plugin add-on (scheduler).
TYPE is what addons:create takes. ATTACHMENT is the --as name used by addons:detach.
`)
	register("addons:list", runAddonsList, `
usage: flynn addons:list

List add-ons on this app.
`)
	register("addons:types", runAddonsTypes, `
usage: flynn addons:types

List add-on types installed plugins provide. Official plugins that are not
installed are omitted. addons:create <type> uses these names.
`)
	register("addons:create", runAddonsCreate, `
usage: flynn addons:create <type> [--as <name>] [--follow <resource>] [--runtime <name>] [--replication <mode>] [--auto-failover] [--cpu <milli>] [--memory <bytes>] [--disk <bytes>] [--unattached] [--owner <handle>]

Provision an add-on of <type>. Options after <type> come from the installed
plugin. --runtime is offered only when that type declares runtimes. Omitting
it uses the type's default. --cpu, --memory, and --disk are rejected unless
the type allows custom sizes and a cluster admin has allowed them. They
override the runtime's values (default small) one at a time, so --memory
alone keeps that runtime's CPU and disk; the instance's runtime is then
"custom". Each engine has a memory minimum (redis 64MB, postgres 128MB,
mysql 256MB, mongodb and kafka 512MB, clickhouse 1GB) and --cpu is at
least 100.

With no -a and no flynn git remote, the resource is created without an app.
--unattached does that even when an app is selected. It is owned by --owner
when that is set, otherwise by flynn context. Both accept a user or an
organization handle. With neither, it is owned by your personal account.
Attach it later with flynn -a <app> addons:attach <name>. The app must use
the same account.

Options:
	--owner=<handle>  User or organization that owns an unattached resource

The command returns after a database instance is scheduled. The datastore
keeps starting in the background.

Examples:

	$ flynn addons:create postgres
	$ flynn addons:create postgres --unattached
	$ flynn addons:create postgres --unattached --owner acme
	$ flynn addons:create postgres --as ANALYTICS
	$ flynn addons:create postgres --as AMBER
	$ flynn addons:create redis --cpu 300 --memory 384MB
	$ flynn addons:create scheduler
`)
	register("addons:attach", runAddonsAttach, `
usage: flynn addons:attach <addon> [--as <name>]

Attach an existing add-on to this app. <addon> is the NAME from addons:list
on the app that owns it. The add-on must already exist on another app in the
same account. --as sets the attachment name (ANALYTICS or AMBER).

Options:
	--as=<name>  attachment name (NAME becomes NAME_URL; a color short name becomes the type's color URL)

Examples:

	$ flynn -a shop addons:attach postgresql-concave-48291
	$ flynn -a shop addons:attach postgresql-concave-48291 --as ANALYTICS
	$ flynn -a shop addons:attach postgresql-concave-48291 --as AMBER
`)
	register("addons:detach", runAddonsDetach, `
usage: flynn addons:detach <attachment>

Remove an attachment from this app. <attachment> is the --as name (ANALYTICS
or AMBER), or the add-on NAME or ID (postgresql-concave-48291), which removes
every attachment of that add-on from this app. It is not the env var. The
add-on is not deleted. The app that owns the add-on uses addons:destroy.

Examples:

	$ flynn -a shop addons:detach ANALYTICS
	$ flynn -a shop addons:detach postgresql-concave-48291
`)
	register("addons:destroy", runAddonsDestroy, `
usage: flynn addons:destroy [<provider>] [<addon>] [--yes]

Delete an add-on this app owns. <addon> is the NAME from addons:list
(postgresql-concave-48291, or scheduler). flynn resource remove postgres
<name> passes the provider and the NAME or ID. An app that is only attached
uses addons:detach. With no app selected (no -a and no flynn git remote),
<addon> names a resource that no app uses, such as one made with
addons:create --unattached.

The provider is asked first and can refuse: a database leader cannot be
removed while followers are still linked. The app's env is changed only
after the provider accepted. The attachment env is then removed from this
app and from every other attached app; shared keys that another attached
resource still provides (FLYNN_MYSQL_TLS_ENABLED, its ROLE) stay. An isolated
instance is destroyed in the background.

Destroying scheduler, after confirmation, stops and deletes that app's
scheduled jobs.

Options:
	--yes  skip the confirmation for plugin add-ons such as scheduler
`)
	register("addons:expose", runAddonsExpose, `
usage: flynn addons:expose <addon> [--domain <host>] [-p <port>] [--tls-mode <mode>] [--auto-tls] [-c <tls-cert> -k <tls-key>]

Export one add-on on a TCP route. <addon> is the NAME from addons:list.
Only types whose plugin opts in can be exposed. The route port is the lowest
free port in 10000-29999 unless -p is set. -p must be inside that window.
The cluster firewall opens that one port on every host. The printed URL uses
the node IP and the route port. Running this again prints the same URL.

Options:
	--domain=<host>        hostname (default: SERVICE.CLUSTERDOMAIN)
	-p, --port=<port>      TCP port in 10000-29999 (lowest free port if omitted)
	--tls-mode=<mode>      off, passthrough, or terminate
	--auto-tls             Let's Encrypt on a terminate route (requires ACME)
	-c, --tls-cert=<tls-cert>  PEM cert for tls_mode=terminate, - for stdin
	-k, --tls-key=<tls-key>    PEM key for tls_mode=terminate, - for stdin
`)
	register("addons:unexpose", runAddonsUnexpose, `
usage: flynn addons:unexpose <addon> [--domain <host>]

Remove the TCP export route for one add-on. The cluster firewall closes that
port on its own.

Options:
	--domain=<host>  hostname of the route to remove (default: the add-on hostname)
`)
}

// addonsNoApp is true when no app is selected (no -a, no remote, no
// FLYNN_APP). Tests replace it.
var addonsNoApp = func() bool {
	_, err := app()
	return err != nil
}

// confirmAddonDestroy is replaced in tests.
var confirmAddonDestroy = promptYesNo

func runAddonsList(args *docopt.Args, client controller.Client) error {
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return err
	}
	attachments, _ := loadAddonAttachments(client)
	releaseEnv := map[string]string{}
	if rel, err := client.GetAppRelease(mustApp()); err == nil && rel != nil {
		releaseEnv = rel.Env
	}
	w := tabWriter()
	defer w.Flush()
	listRec(w, "NAME", "TYPE", "ATTACHMENT")
	for _, res := range resources {
		if res == nil {
			continue
		}
		p, err := client.GetProvider(res.ProviderID)
		if err != nil {
			return err
		}
		spec := cat.AddonByProvider(p.Name)
		typ := p.Name
		prefix, conventional := "", ""
		if spec != nil {
			typ = spec.Type
			if spec.Env != nil {
				prefix = spec.Env.Prefix
				conventional = spec.Env.Conventional
			}
		}
		names := attachmentNamesFor(attachments, res, releaseEnv, prefix, conventional)
		listRec(w, resourceDisplayName(res), typ, strings.Join(names, ", "))
	}
	plugins, err := client.ListPluginAddons(mustApp())
	if err != nil && !errors.Is(err, controller.ErrNotFound) {
		return err
	}
	for _, addon := range plugins {
		if addon == nil {
			continue
		}
		listRec(w, addon.Name, addon.Type, "")
	}
	return nil
}

func runAddonsTypes(args *docopt.Args, client controller.Client) error {
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	rtCat, err := databaseRuntimeCatalog(client)
	if err != nil {
		return err
	}
	w := tabWriter()
	defer w.Flush()
	listRec(w, "TYPE", "DESCRIPTION", "VERBS", "RUNTIMES")
	addons := append([]plugin.AddonSpec(nil), cat.Addons...)
	sort.Slice(addons, func(i, j int) bool { return addons[i].Type < addons[j].Type })
	for _, spec := range addons {
		listRec(w, spec.Type, spec.Description, strings.Join(spec.Verbs, ", "), spec.RuntimeSummary(&rtCat))
	}
	return nil
}

// unattachedOwnerHandle is the account to resolve for a resource created
// with no app. --owner wins over flynn context. Empty means the personal
// account. A handle may name a user or an organization.
func unattachedOwnerHandle(ownerFlag, contextHandle string) string {
	if h := strings.TrimSpace(ownerFlag); h != "" {
		return h
	}
	return strings.TrimSpace(contextHandle)
}

func unattachedResourceOwner(client controller.Client, ownerFlag string) (string, error) {
	contextHandle := ""
	if clusterConf != nil {
		contextHandle = clusterConf.Context
	}
	if handle := unattachedOwnerHandle(ownerFlag, contextHandle); handle != "" {
		return resolveOwner(client, handle)
	}
	return currentAccount(client)
}

// addonCreateApps is empty when the resource should exist before any app.
func addonCreateApps(unattached bool, appName string) []string {
	appName = strings.TrimSpace(appName)
	if unattached || appName == "" {
		return nil
	}
	return []string{appName}
}

func runAddonsCreate(args *docopt.Args, client controller.Client) error {
	typ := strings.TrimSpace(args.String["<type>"])
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	spec := cat.AddonByType(typ)
	if spec == nil {
		return missingAddonType(typ)
	}
	if !spec.Supports(plugin.VerbCreate) {
		return spec.VerbError(plugin.VerbCreate)
	}
	if spec.Backend == plugin.BackendPlugin {
		if _, err := client.PutPluginAddon(mustApp(), spec.Type); err != nil {
			return err
		}
		log.Printf("Added %s add-on to %s.", spec.Type, mustApp())
		return nil
	}
	unattached := args.Bool["--unattached"]
	appName := ""
	if !unattached {
		name, err := app()
		if err != nil {
			unattached = true
		} else {
			appName = name
		}
	}
	rtCat, err := databaseRuntimeCatalog(client)
	if err != nil {
		return err
	}
	follow, err := resolvePeerRef(client, appName, spec.Provider, args.String["--follow"])
	if err != nil {
		return err
	}
	raw, err := spec.ProvisionConfig(plugin.CreateInput{
		As:           args.String["--as"],
		Follow:       follow,
		Runtime:      args.String["--runtime"],
		Replication:  args.String["--replication"],
		CPU:          args.String["--cpu"],
		Memory:       args.String["--memory"],
		Disk:         args.String["--disk"],
		AutoFailover: args.Bool["--auto-failover"],
	}, rtCat)
	if err != nil {
		return err
	}
	msg := json.RawMessage(raw)
	req := &ct.ResourceReq{ProviderID: spec.Provider, Apps: addonCreateApps(unattached, appName), Config: &msg}
	ownerFlag := strings.TrimSpace(args.String["--owner"])
	if ownerFlag != "" && !unattached {
		return fmt.Errorf("--owner is for a resource created without an app; an attached app keeps its own account")
	}
	if unattached {
		account, err := unattachedResourceOwner(client, ownerFlag)
		if err != nil {
			return err
		}
		req.OwnerAccount = account
	}
	res, err := client.ProvisionResource(req)
	if err != nil {
		return err
	}
	if unattached {
		log.Println(createdResourceMessage(res, ""))
		log.Printf("Not attached to an app. Attach it later with flynn -a <app> addons:attach %s", resourceDisplayName(res))
		log.Println(resourceStartingMessage(res, spec.Wait))
		return nil
	}
	env := appliedAttachmentEnv(client, res.Env, args.String["--as"], true)
	if _, err := setEnv(client, "", env); err != nil {
		return err
	}
	if err := recordAddonAttachment(client, spec, res, env); err != nil {
		return err
	}
	log.Println(createdResourceMessage(res, args.String["--as"]))
	log.Println(resourceStartingMessage(res, spec.Wait))
	return nil
}

func runAddonsAttach(args *docopt.Args, client controller.Client) error {
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	res, spec, err := lookupAddonByName(client, cat, args.String["<addon>"])
	if err != nil {
		return err
	}
	if spec == nil || !spec.Supports(plugin.VerbAttach) {
		if spec != nil {
			return spec.VerbError(plugin.VerbAttach)
		}
		return fmt.Errorf("%s addons do not support attach", args.String["<addon>"])
	}
	appID, err := resolvedAppID(client)
	if err != nil {
		return err
	}
	attached, err := client.AddResourceApp(spec.Provider, res.ID, appID)
	if err != nil {
		if !alreadyAttachedError(err) {
			return err
		}
		attached = res
	}
	env := appliedAttachmentEnv(client, attached.Env, args.String["--as"], false)
	if len(env) == 0 {
		return fmt.Errorf("%s is already attached to this app; use --as to attach it under another name", resourceDisplayName(res))
	}
	if _, err := setEnv(client, "", env); err != nil {
		return err
	}
	if err := recordAddonAttachment(client, spec, res, env); err != nil {
		return err
	}
	log.Printf("Attached add-on %s.", resourceDisplayName(res))
	return nil
}

func runAddonsDetach(args *docopt.Args, client controller.Client) error {
	raw := strings.TrimSpace(args.String["<attachment>"])
	name := canonicalAttachmentName(raw)
	if name == "" {
		return fmt.Errorf("attachment name is required")
	}
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	stored, err := loadAddonAttachments(client)
	if err != nil {
		return err
	}
	if rec, ok := stored[name]; ok {
		return detachStoredAttachment(client, cat, name, rec)
	}
	err = detachInferredAttachment(client, cat, name)
	if !errors.Is(err, errAttachmentNotFound) {
		return err
	}
	// Not an attachment name: the add-on NAME or ID, as addons:attach takes,
	// among this app's own resources.
	res, spec, lerr := lookupAppAddonByName(client, cat, raw)
	var ambiguous *ambiguousResourceError
	if errors.As(lerr, &ambiguous) {
		return lerr
	}
	if lerr != nil || res == nil {
		return fmt.Errorf("attachment %q not found on this app; use the --as name or the add-on NAME from flynn addons", raw)
	}
	return detachResourceAddon(client, spec, res)
}

var errAttachmentNotFound = errors.New("attachment not found")

// detachResourceAddon removes every attachment of res from this app: it
// unbinds the app (the add-on stays), unsets the env and drops the records.
// The owning app destroys instead.
func detachResourceAddon(client controller.Client, spec *plugin.AddonSpec, res *ct.Resource) error {
	if spec != nil && !spec.Supports(plugin.VerbDetach) {
		return spec.VerbError(plugin.VerbDetach)
	}
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	if res.OwnedByApp(app) {
		return fmt.Errorf("%s belongs to this app; flynn addons:destroy %s deletes it (addons:detach is for an app attached to another app's add-on)", resourceDisplayName(res), resourceDisplayName(res))
	}
	if _, err := client.DeleteResourceApp(resourceProviderName(client, spec, res), res.ID, app.ID); err != nil {
		return err
	}
	if err := removeAttachmentFromApp(client, app.ID, res); err != nil {
		return fmt.Errorf("unbound %s, but removing its env from %s failed: %w", resourceDisplayName(res), app.Name, err)
	}
	log.Printf("Detached %s.", resourceDisplayName(res))
	return nil
}

func runAddonsDestroy(args *docopt.Args, client controller.Client) error {
	provider, name, err := normalizeResourceArgs(args.String["<provider>"], args.String["<addon>"])
	if err != nil {
		return err
	}
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	if addonsNoApp() {
		return destroyUnattachedAddon(client, cat, provider, name)
	}
	if provider != "" {
		res, err := lookupProviderResource(client, mustApp(), provider, name)
		if err != nil {
			return err
		}
		return destroyResourceAddon(args, client, res, specForResource(client, cat, res))
	}
	res, spec, err := lookupAppAddonByName(client, cat, name)
	if err == nil && res != nil {
		return destroyResourceAddon(args, client, res, spec)
	}
	var ambiguous *ambiguousResourceError
	if errors.As(err, &ambiguous) {
		return err
	}
	if spec := cat.AddonByType(name); spec != nil && spec.Backend == plugin.BackendPlugin {
		if !spec.Supports(plugin.VerbDestroy) {
			return spec.VerbError(plugin.VerbDestroy)
		}
		if !args.Bool["--yes"] {
			if !confirmAddonDestroy(fmt.Sprintf("Remove the %s add-on from this app and delete its scheduled jobs?", spec.Type)) {
				return nil
			}
		}
		if err := client.DeletePluginAddon(mustApp(), spec.Type); err != nil {
			return err
		}
		log.Printf("Removed %s add-on from %s.", spec.Type, mustApp())
		return nil
	}
	return fmt.Errorf("add-on %q not found; see flynn addons", name)
}

// destroyUnattachedAddon deletes a resource that no app uses (one made with
// addons:create --unattached) when no app is selected. A resource that is
// still attached must be destroyed from its owning app, which also removes
// the env from every attached app.
func destroyUnattachedAddon(client controller.Client, cat *plugin.Catalog, provider, name string) error {
	var res *ct.Resource
	var err error
	if provider != "" {
		res, err = lookupProviderResource(client, "", provider, name)
	} else {
		res, err = lookupResourceByRef(client, name)
	}
	if err != nil {
		return err
	}
	spec := specForResource(client, cat, res)
	if spec != nil && !spec.Supports(plugin.VerbDestroy) {
		return spec.VerbError(plugin.VerbDestroy)
	}
	if len(res.Apps) > 0 {
		owner := strings.TrimSpace(res.OwnerApp)
		if owner == "" {
			owner = res.Apps[len(res.Apps)-1]
		}
		if app, err := client.GetApp(owner); err == nil && app != nil && app.Name != "" {
			owner = app.Name
		}
		return fmt.Errorf("%s is attached to an app; run flynn -a %s addons:destroy %s", resourceDisplayName(res), owner, resourceDisplayName(res))
	}
	deleted, err := client.DeleteResource(resourceProviderName(client, spec, res), res.ID)
	if err != nil {
		return err
	}
	log.Printf("Deleted add-on %s.", resourceDisplayName(deleted))
	log.Println("The instance is being removed in the background.")
	return nil
}

func resourceProviderName(client controller.Client, spec *plugin.AddonSpec, res *ct.Resource) string {
	if spec != nil && spec.Provider != "" {
		return spec.Provider
	}
	if p, err := client.GetProvider(res.ProviderID); err == nil && p != nil && p.Name != "" {
		return p.Name
	}
	return res.ProviderID
}

func runAddonsExpose(args *docopt.Args, client controller.Client) error {
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	res, spec, err := lookupAppAddonByName(client, cat, args.String["<addon>"])
	if err != nil {
		return err
	}
	if spec == nil || !spec.Supports(plugin.VerbExpose) {
		if spec != nil {
			return spec.VerbError(plugin.VerbExpose)
		}
		return fmt.Errorf("%s addons cannot be exposed", args.String["<addon>"])
	}
	mode := strings.TrimSpace(args.String["--tls-mode"])
	if mode == "" && spec.DefaultTLS() != "" {
		mode = spec.DefaultTLS()
		args.String["--tls-mode"] = mode
	}
	if !spec.TLSModeAllowed(mode) {
		return fmt.Errorf("%s addons do not support tls mode %q", spec.Type, mode)
	}
	args.String["<resource>"] = resourceDisplayName(res)
	return runResourceExpose(args, client)
}

func runAddonsUnexpose(args *docopt.Args, client controller.Client) error {
	cat, err := clusterPluginCatalog()
	if err != nil {
		return err
	}
	res, spec, err := lookupAppAddonByName(client, cat, args.String["<addon>"])
	if err != nil {
		return err
	}
	if spec == nil || !spec.Supports(plugin.VerbUnexpose) {
		if spec != nil {
			return spec.VerbError(plugin.VerbUnexpose)
		}
		return fmt.Errorf("%s addons cannot be exposed", args.String["<addon>"])
	}
	args.String["<resource>"] = resourceDisplayName(res)
	return runResourceUnexpose(args, client)
}

// destroyResourceAddon asks the provider to delete res first. The provider
// can refuse (a leader with followers, a datastore guard), and the tenant's
// env must then stay as it was. Only after the delete is accepted is the
// attachment env removed, from the owning app and from every other attached
// app, keeping shared keys a remaining resource still provides.
func destroyResourceAddon(args *docopt.Args, client controller.Client, res *ct.Resource, spec *plugin.AddonSpec) error {
	if spec != nil && !spec.Supports(plugin.VerbDestroy) {
		return spec.VerbError(plugin.VerbDestroy)
	}
	var keys []string
	if spec != nil && spec.Destroy != nil {
		keys = spec.Destroy.FollowerEnv
	}
	if names := resourceFollowerNames(res, appResourcesOrNil(client, mustApp()), keys); len(names) > 0 {
		return fmt.Errorf("cannot remove %s while followers are still linked (%s); unfollow or remove those add-ons first", resourceDisplayName(res), strings.Join(names, ", "))
	}
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	if !res.OwnedByApp(app) {
		return fmt.Errorf("cannot delete %s from %s; this app is attached but does not own the add-on. Use flynn addons:detach", resourceDisplayName(res), app.Name)
	}
	deleted, err := client.DeleteResource(resourceProviderName(client, spec, res), res.ID)
	if err != nil {
		return err
	}
	if deleted == nil {
		deleted = res
	}
	apps := []string{app.ID}
	for _, id := range res.Apps {
		if id = strings.TrimSpace(id); id != "" && !strings.EqualFold(id, app.ID) && !strings.EqualFold(id, app.Name) {
			apps = append(apps, id)
		}
	}
	var ownerErr error
	for i, ref := range apps {
		if err := removeAttachmentFromApp(client, ref, res); err != nil {
			if i == 0 {
				ownerErr = fmt.Errorf("deleted add-on %s, but removing its env from %s failed: %w", resourceDisplayName(res), app.Name, err)
				continue
			}
			log.Printf("Deleted add-on %s, but removing its env from app %s failed: %v", resourceDisplayName(res), ref, err)
		}
	}
	if ownerErr != nil {
		return ownerErr
	}
	log.Printf("Deleted add-on %s.", resourceDisplayName(deleted))
	log.Println("The instance is being removed in the background.")
	return nil
}

// removeAttachmentFromApp unsets res's attachment env on one app (keeping
// shared keys its other resources provide) and drops res from that app's
// attachment record. An app with no release has no env to change.
func removeAttachmentFromApp(client controller.Client, appRef string, res *ct.Resource) error {
	release, err := client.GetAppRelease(appRef)
	if err != nil && !errors.Is(err, controller.ErrNotFound) {
		return err
	}
	if release != nil {
		var remaining []map[string]string
		if list, err := client.AppResourceList(appRef); err == nil {
			for _, r := range list {
				if r != nil && r.ID != res.ID {
					remaining = append(remaining, r.Env)
				}
			}
		}
		if env := resname.UnsetAttachmentKeeping(release.Env, res.Env, remaining); len(env) > 0 {
			if _, err := setAppEnv(client, appRef, "", env); err != nil {
				return err
			}
		}
	}
	return forgetAppAddonAttachments(client, appRef, res.ID, "")
}

func missingAddonType(typ string) error {
	return fmt.Errorf("%s is not installed on this cluster. Operators: flynn-host plugin:install %s", typ, typ)
}

func lookupAddonByName(client controller.Client, cat *plugin.Catalog, name string) (*ct.Resource, *plugin.AddonSpec, error) {
	res, err := lookupResourceByRef(client, name)
	if err != nil {
		return nil, nil, err
	}
	return res, specForResource(client, cat, res), nil
}

func lookupAppAddonByName(client controller.Client, cat *plugin.Catalog, name string) (*ct.Resource, *plugin.AddonSpec, error) {
	res, err := lookupAppResourceStrict(client, mustApp(), name)
	if err != nil {
		return nil, nil, err
	}
	return res, specForResource(client, cat, res), nil
}

func specForResource(client controller.Client, cat *plugin.Catalog, res *ct.Resource) *plugin.AddonSpec {
	if res == nil || client == nil || cat == nil {
		return nil
	}
	p, err := client.GetProvider(res.ProviderID)
	if err != nil || p == nil {
		return nil
	}
	return cat.AddonByProvider(p.Name)
}

type addonAttachment struct {
	Addon      string `json:"addon"`
	ResourceID string `json:"resource_id,omitempty"`
	EnvKey     string `json:"env_key"`
	Type       string `json:"type,omitempty"`
}

func canonicalAttachmentName(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

func loadAddonAttachments(client controller.Client) (map[string]addonAttachment, error) {
	return loadAppAddonAttachments(client, mustApp())
}

func loadAppAddonAttachments(client controller.Client, appRef string) (map[string]addonAttachment, error) {
	out := map[string]addonAttachment{}
	app, err := client.GetApp(appRef)
	if err != nil {
		return nil, err
	}
	raw := ""
	if app.Meta != nil {
		raw = app.Meta[metaAddonAttachments]
	}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]addonAttachment{}, nil
	}
	return out, nil
}

func saveAddonAttachments(client controller.Client, attachments map[string]addonAttachment) error {
	return saveAppAddonAttachments(client, mustApp(), attachments)
}

func saveAppAddonAttachments(client controller.Client, appRef string, attachments map[string]addonAttachment) error {
	app, err := client.GetApp(appRef)
	if err != nil {
		return err
	}
	if app.Meta == nil {
		app.Meta = map[string]string{}
	}
	if len(attachments) == 0 {
		delete(app.Meta, metaAddonAttachments)
	} else {
		raw, err := json.Marshal(attachments)
		if err != nil {
			return err
		}
		app.Meta[metaAddonAttachments] = string(raw)
	}
	return client.UpdateAppMeta(app)
}

func recordAddonAttachment(client controller.Client, spec *plugin.AddonSpec, res *ct.Resource, env map[string]*string) error {
	prefix, conventional := "", ""
	if spec != nil && spec.Env != nil {
		prefix = spec.Env.Prefix
		conventional = spec.Env.Conventional
	}
	name, key := attachmentIdentity(env, prefix, conventional)
	if name == "" || key == "" || res == nil {
		return nil
	}
	stored, err := loadAddonAttachments(client)
	if err != nil {
		return err
	}
	rec := addonAttachment{Addon: resourceDisplayName(res), ResourceID: res.ID, EnvKey: key}
	if spec != nil {
		rec.Type = spec.Type
	}
	stored[canonicalAttachmentName(name)] = rec
	return saveAddonAttachments(client, stored)
}

func attachmentIdentity(env map[string]*string, prefix, conventional string) (string, string) {
	var colorKey, namedKey string
	for k, v := range env {
		if v == nil || strings.TrimSpace(*v) == "" || !strings.HasSuffix(k, "_URL") {
			continue
		}
		if conventional != "" && k == conventional {
			continue
		}
		if prefix != "" && strings.HasPrefix(k, prefix) {
			colorKey = k
			continue
		}
		if !strings.HasPrefix(k, "FLYNN_") {
			namedKey = k
		}
	}
	if namedKey != "" {
		return strings.TrimSuffix(namedKey, "_URL"), namedKey
	}
	if colorKey != "" {
		return strings.TrimSuffix(strings.TrimPrefix(colorKey, prefix), "_URL"), colorKey
	}
	return "", ""
}

func attachmentNamesFor(stored map[string]addonAttachment, res *ct.Resource, release map[string]string, prefix, conventional string) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		name = canonicalAttachmentName(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	if res != nil {
		for name, rec := range stored {
			if rec.ResourceID == res.ID || rec.Addon == resourceDisplayName(res) {
				add(name)
			}
		}
	}
	if len(names) == 0 && res != nil {
		for k, v := range release {
			if !strings.HasSuffix(k, "_URL") || v == "" {
				continue
			}
			if !envMatchesResource(res.Env, v) {
				continue
			}
			if conventional != "" && k == conventional {
				continue
			}
			if name, _ := attachmentIdentity(map[string]*string{k: &v}, prefix, conventional); name != "" {
				add(name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func envMatchesResource(resource map[string]string, val string) bool {
	for _, rv := range resource {
		if rv == val {
			return true
		}
	}
	return false
}

func detachStoredAttachment(client controller.Client, cat *plugin.Catalog, name string, rec addonAttachment) error {
	release, err := client.GetAppRelease(mustApp())
	if errors.Is(err, controller.ErrNotFound) {
		release, err = &ct.Release{}, nil
	}
	if err != nil {
		return err
	}
	env := map[string]*string{}
	if rec.EnvKey != "" {
		env[rec.EnvKey] = nil
	}
	remaining := 0
	stored, err := loadAddonAttachments(client)
	if err != nil {
		return err
	}
	delete(stored, name)
	for _, other := range stored {
		if other.ResourceID == rec.ResourceID {
			remaining++
		}
	}
	res, spec, resErr := lookupAppAddonByName(client, cat, firstNonEmpty(rec.Addon, rec.ResourceID))
	if remaining == 0 && resErr == nil && res != nil {
		var others []map[string]string
		for _, r := range appResourcesOrNil(client, mustApp()) {
			if r != nil && r.ID != res.ID {
				others = append(others, r.Env)
			}
		}
		for k, v := range resname.UnsetAttachmentKeeping(release.Env, res.Env, others) {
			env[k] = v
		}
		appID, err := resolvedAppID(client)
		if err != nil {
			return err
		}
		provider := ""
		if spec != nil {
			provider = spec.Provider
		}
		if provider == "" && res != nil {
			if p, err := client.GetProvider(res.ProviderID); err == nil && p != nil {
				provider = p.Name
			}
		}
		if provider != "" {
			if _, err := client.DeleteResourceApp(provider, res.ID, appID); err != nil {
				return err
			}
		}
	}
	if len(env) > 0 {
		if _, err := setEnv(client, "", env); err != nil {
			return err
		}
	}
	if err := saveAddonAttachments(client, stored); err != nil {
		return err
	}
	log.Printf("Detached %s.", name)
	return nil
}

func detachInferredAttachment(client controller.Client, cat *plugin.Catalog, name string) error {
	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease(mustApp())
	if err != nil && !errors.Is(err, controller.ErrNotFound) {
		return err
	}
	envMap := map[string]string{}
	if release != nil {
		envMap = release.Env
	}
	for _, res := range resources {
		spec := specForResource(client, cat, res)
		prefix, conventional := "", ""
		if spec != nil && spec.Env != nil {
			prefix = spec.Env.Prefix
			conventional = spec.Env.Conventional
		}
		for k, v := range envMap {
			if !envMatchesResource(res.Env, v) {
				continue
			}
			got, _ := attachmentIdentity(map[string]*string{k: &v}, prefix, conventional)
			if !strings.EqualFold(got, name) {
				continue
			}
			rec := addonAttachment{Addon: resourceDisplayName(res), ResourceID: res.ID, EnvKey: k}
			if spec != nil {
				rec.Type = spec.Type
			}
			return detachStoredAttachment(client, cat, canonicalAttachmentName(name), rec)
		}
	}
	return errAttachmentNotFound
}

func forgetAppAddonAttachments(client controller.Client, appRef, resourceID, name string) error {
	stored, err := loadAppAddonAttachments(client, appRef)
	if err != nil {
		return err
	}
	changed := false
	for k, rec := range stored {
		if name != "" && k == canonicalAttachmentName(name) {
			delete(stored, k)
			changed = true
			continue
		}
		if resourceID != "" && rec.ResourceID == resourceID {
			delete(stored, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return saveAppAddonAttachments(client, appRef, stored)
}

// addonsArgUsage narrows addons:create to the installed type's docopt and
// applies plugin reject/requires rules before parse.
func addonsArgUsage(name string, args []string) (string, error) {
	if name != "addons:create" {
		return "", nil
	}
	typ := ""
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			typ = arg
			break
		}
	}
	if typ == "" {
		return "", nil
	}
	cat, err := clusterPluginCatalog()
	if err != nil || cat == nil {
		return "", nil
	}
	spec := cat.AddonByType(typ)
	if spec == nil {
		return "usage: flynn addons:create <type> [<args>...]", nil
	}
	if err := spec.CheckArgv(args); err != nil {
		return "", err
	}
	return spec.CreateUsage(), nil
}

func addonVerbVisible(cat *plugin.Catalog, verb string) bool {
	if cat == nil || len(cat.Addons) == 0 {
		return true
	}
	switch verb {
	case "list", "types", "help":
		return true
	default:
		return cat.SupportsVerb(verb)
	}
}

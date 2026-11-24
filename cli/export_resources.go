package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/backup"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/resname"
)

// An app export must never leave out a database without saying so. Every
// resource attached to the app is either dumped into the archive or makes
// the export fail, unless --skip-databases is passed, which warns and records
// the resource in resources.json. Import then never copies a resource's
// connection env from the archive: the copy gets a new resource holding the
// restored data, or no connection at all.

type exportOptions struct {
	// SkipDatabases exports without the data of attached resources that
	// the export cannot dump, instead of failing.
	SkipDatabases bool
}

// exportResourceManifest is resources.json in an app export.
type exportResourceManifest struct {
	Resources []exportResource `json:"resources"`
	// DatabasesSkipped names the resources whose data is not in the
	// archive because the export ran with --skip-databases.
	DatabasesSkipped []string `json:"databases-skipped,omitempty"`
}

type exportResource struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ID       string `json:"id,omitempty"`
	// EnvKeys are the release env keys that belong to this resource's
	// attachment. Import drops them.
	EnvKeys []string `json:"env_keys,omitempty"`
	// Data is the archive entry holding the resource's data, relative to
	// the app directory (postgres.dump, mysql.dump, resources/<name>.dump).
	Data    string `json:"data,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	// Hint is how to back up a resource the export cannot include.
	Hint string `json:"hint,omitempty"`
}

const (
	exportManifestEntry = "resources.json"
	exportPostgresEntry = "postgres.dump"
	exportMysqlEntry    = "mysql.dump"
	exportResourceDir   = "resources/"
)

// exportPluginDumpProviders are the providers whose plugin dump action
// streams the whole dataset to stdout and whose restore action takes that
// stream back into a fresh resource, so retrying a restore while the new
// instance starts is safe. Postgres, MongoDB, ClickHouse and Kafka dump into
// the cluster blobstore, not to a stream. MySQL's restore replays SQL, which
// a retry could apply twice.
var exportPluginDumpProviders = map[string]bool{"redis": true}

var (
	// exportRunJob runs plugin dump and restore jobs. Tests replace it.
	exportRunJob            = runJob
	exportLogf              = log.Printf
	importRestoreTimeout    = 5 * time.Minute
	importRestoreRetryDelay = 5 * time.Second
)

type pluginResourceDump struct {
	spec   *plugin.CLI
	action *plugin.CLIAction
	name   string
	entry  string
}

// exportPlan is decided before anything is written, so an export that would
// leave out a database fails before it downloads layers.
type exportPlan struct {
	manifest    exportResourceManifest
	legacyPG    *runConfig
	legacyMySQL *runConfig
	pluginDumps []pluginResourceDump
}

func planExport(client controller.Client, appName string, release *ct.Release, opts exportOptions) (*exportPlan, error) {
	plan := &exportPlan{}
	// The built-in appliances (FLYNN_POSTGRES / FLYNN_MYSQL on the app
	// release) keep the dump they always had.
	if release.Env["FLYNN_POSTGRES"] != "" {
		if config, err := getPgRunConfig(client, appName, release); err == nil {
			plan.legacyPG = config
		}
	}
	if release.Env["FLYNN_MYSQL"] != "" {
		if config, err := getMysqlRunConfig(client, appName, release); err == nil {
			plan.legacyMySQL = config
		}
	}

	resources, err := client.AppResourceList(appName)
	if err != nil {
		return nil, fmt.Errorf("error listing the app's resources: %s", err)
	}
	var (
		cat       *plugin.Catalog
		catErr    error
		catLoaded bool
		providers = map[string]string{}
		missing   []exportResource
	)
	for _, res := range resources {
		if res == nil {
			continue
		}
		provider, ok := providers[res.ProviderID]
		if !ok {
			p, err := client.GetProvider(res.ProviderID)
			if err != nil {
				return nil, fmt.Errorf("error getting provider of resource %s: %s", resourceDisplayName(res), err)
			}
			provider = p.Name
			providers[res.ProviderID] = provider
		}
		entry := exportResource{
			Name:     resourceDisplayName(res),
			Provider: provider,
			ID:       res.ID,
			EnvKeys:  attachmentEnvKeys(release.Env, res.Env),
		}
		switch {
		case plan.legacyPG != nil && sameIdentity(res.Env, release.Env, "FLYNN_POSTGRES"):
			entry.Data = exportPostgresEntry
		case plan.legacyMySQL != nil && sameIdentity(res.Env, release.Env, "FLYNN_MYSQL"):
			entry.Data = exportMysqlEntry
		default:
			if !catLoaded {
				cat, catErr = clusterPluginCatalog()
				catLoaded = true
			}
			dump, hint := pluginDumpFor(cat, catErr, res, provider, appName, entry.Name)
			if dump != nil {
				entry.Data = dump.entry
				plan.pluginDumps = append(plan.pluginDumps, *dump)
			} else {
				entry.Hint = hint
				missing = append(missing, entry)
			}
		}
		plan.manifest.Resources = append(plan.manifest.Resources, entry)
	}

	if len(missing) == 0 {
		return plan, nil
	}
	if !opts.SkipDatabases {
		var b strings.Builder
		fmt.Fprintf(&b, "the export cannot include the data of %d resource(s) attached to %s:", len(missing), appName)
		for _, m := range missing {
			fmt.Fprintf(&b, "\n  %s (provider %s): %s", m.Name, m.Provider, m.Hint)
		}
		b.WriteString("\nBack the data up that way, then rerun with --skip-databases to export without it.")
		return nil, fmt.Errorf("%s", b.String())
	}
	for i := range plan.manifest.Resources {
		r := &plan.manifest.Resources[i]
		if r.Data != "" {
			continue
		}
		r.Skipped = true
		plan.manifest.DatabasesSkipped = append(plan.manifest.DatabasesSkipped, r.Name)
		exportLogf("WARNING: --skip-databases: the export does not include the data of %s (provider %s): %s", r.Name, r.Provider, r.Hint)
	}
	return plan, nil
}

func sameIdentity(resourceEnv, releaseEnv map[string]string, key string) bool {
	v := strings.TrimSpace(resourceEnv[key])
	return v != "" && v == strings.TrimSpace(releaseEnv[key])
}

// attachmentEnvKeys are the release env keys addons:destroy would unset for
// this resource.
func attachmentEnvKeys(releaseEnv, resourceEnv map[string]string) []string {
	unset := resname.UnsetAttachment(releaseEnv, resourceEnv)
	keys := make([]string, 0, len(unset))
	for k := range unset {
		if _, ok := releaseEnv[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func pluginCLIForResource(cat *plugin.Catalog, res *ct.Resource) *plugin.CLI {
	if cat == nil {
		return nil
	}
	for i := range cat.Commands {
		if pluginResourceMatches(&cat.Commands[i], res) {
			return &cat.Commands[i]
		}
	}
	return nil
}

func pluginAction(spec *plugin.CLI, name string) *plugin.CLIAction {
	if spec == nil {
		return nil
	}
	return spec.MatchScopedAction(map[string]bool{name: true}, plugin.CLIScopeApp, plugin.CLIScopeAccount)
}

// pluginDumpFor is the dump export runs for a plugin resource, or nil and
// how to back the resource up by hand.
func pluginDumpFor(cat *plugin.Catalog, catErr error, res *ct.Resource, provider, appName, name string) (*pluginResourceDump, string) {
	if catErr != nil {
		return nil, fmt.Sprintf("the plugin catalog could not be read (%s), so the export cannot dump it", catErr)
	}
	spec := pluginCLIForResource(cat, res)
	if spec == nil {
		return nil, "no installed plugin CLI publishes a dump for this provider"
	}
	dump := pluginAction(spec, "dump")
	if dump == nil || len(dump.Args) == 0 {
		return nil, fmt.Sprintf("flynn %s has no dump command", spec.Command)
	}
	restore := pluginAction(spec, "restore")
	if exportPluginDumpProviders[provider] && dump.StdoutFile != "" && restore != nil && restore.StdinFile != "" {
		return &pluginResourceDump{spec: spec, action: dump, name: name, entry: exportResourceDir + name + ".dump"}, ""
	}
	if dump.StdoutFile != "" {
		return nil, fmt.Sprintf("dump it with flynn -a %s %s:dump %s %s <file> and restore that file into the imported app's new resource", appName, spec.Command, name, dump.StdoutFile)
	}
	return nil, fmt.Sprintf("dump it with flynn -a %s %s:dump %s (the dump is stored in the cluster blobstore, not in this export)", appName, spec.Command, name)
}

func pluginJobArgs(name string) *docopt.Args {
	return &docopt.Args{String: map[string]string{"<name>": name}, Bool: map[string]bool{}}
}

// writePluginDump runs the plugin's dump job and writes its stdout to entry.
func writePluginDump(client controller.Client, tw *backup.TarWriter, d pluginResourceDump, bar backup.ProgressBar) error {
	config, err := pluginJobConfig(client, d.spec, d.action, pluginJobArgs(d.name))
	if err != nil {
		return fmt.Errorf("error dumping %s: %s", d.name, err)
	}
	f, err := ioutil.TempFile("", "flynn-export-resource-")
	if err != nil {
		return fmt.Errorf("error creating temp file: %s", err)
	}
	defer f.Close()
	defer os.Remove(f.Name())

	var stderr bytes.Buffer
	config.ReleaseEnv = d.action.ReleaseEnv
	config.Data = d.action.Data
	config.Exit = false
	config.Stdin = strings.NewReader("")
	config.Stdout = f
	if bar != nil {
		config.Stdout = io.MultiWriter(f, bar)
	}
	config.Stderr = &stderr
	if err := exportRunJob(client, *config); err != nil {
		return fmt.Errorf("error dumping %s with flynn %s:dump: %s%s", d.name, d.spec.Command, err, stderrDetail(&stderr))
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("error getting size of the %s dump: %s", d.name, err)
	}
	if size == 0 {
		return fmt.Errorf("error dumping %s: flynn %s:dump wrote nothing%s", d.name, d.spec.Command, stderrDetail(&stderr))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("error seeking the %s dump: %s", d.name, err)
	}
	if err := tw.WriteHeader(d.entry, int(size)); err != nil {
		return fmt.Errorf("error writing header for %s: %s", d.entry, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("error writing %s: %s", d.entry, err)
	}
	return nil
}

func stderrDetail(b *bytes.Buffer) string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	if len(s) > 2000 {
		s = s[len(s)-2000:]
	}
	return ": " + s
}

// databaseEnvKey reports whether an env var from an archive with no
// resources.json (an export made before EDGE-49) is a database connection,
// so import must not copy it.
func databaseEnvKey(k, v string) bool {
	switch k {
	case "FLYNN_POSTGRES", "FLYNN_MYSQL", "FLYNN_REDIS", "FLYNN_MONGO", "FLYNN_MONGODB", "FLYNN_KAFKA", "FLYNN_CLICKHOUSE",
		"DATABASE_URL", "POSTGRES_URL", "REDIS_URL", "MYSQL_URL", "MONGODB_URL", "MONGO_URL", "KAFKA_URL", "CLICKHOUSE_URL",
		"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE",
		"MYSQL_HOST", "MYSQL_PORT", "MYSQL_USER", "MYSQL_PWD", "MYSQL_PASSWORD", "MYSQL_DATABASE",
		"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD":
		return true
	}
	for _, p := range []string{"FLYNN_POSTGRESQL_", "FLYNN_POSTGRES_", "FLYNN_REDIS_", "FLYNN_MYSQL_", "FLYNN_MONGODB_", "FLYNN_MONGO_", "FLYNN_KAFKA_", "FLYNN_CLICKHOUSE_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	if !strings.HasSuffix(k, "_URL") {
		return false
	}
	scheme := strings.ToLower(v)
	if i := strings.Index(scheme, "://"); i > 0 {
		scheme = scheme[:i]
	} else {
		return false
	}
	switch scheme {
	case "postgres", "postgresql", "mysql", "mysql2", "mariadb", "redis", "rediss", "mongodb", "mongodb+srv", "clickhouse", "kafka":
		return true
	}
	return false
}

// importDropDatabaseEnv removes the source app's database connection env from
// the imported release. With a manifest the export named each resource's
// keys; an older archive falls back to databaseEnvKey.
func importDropDatabaseEnv(env map[string]string, manifest *exportResourceManifest) []string {
	drop := map[string]bool{}
	if manifest != nil {
		for _, r := range manifest.Resources {
			for _, k := range r.EnvKeys {
				drop[k] = true
			}
		}
	} else {
		for k, v := range env {
			if databaseEnvKey(k, v) {
				drop[k] = true
			}
		}
	}
	var dropped []string
	for k := range drop {
		if _, ok := env[k]; ok {
			delete(env, k)
			dropped = append(dropped, k)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// restorePluginResource provisions a new resource from the same provider on
// the imported app and restores the archived dump into it. It returns the
// attachment env for the new release.
func restorePluginResource(client controller.Client, app *ct.App, releaseEnv map[string]string, r exportResource, dump *os.File) (map[string]string, error) {
	cat, err := clusterPluginCatalog()
	if err != nil {
		return nil, fmt.Errorf("error reading the plugin catalog to restore %s: %s", r.Name, err)
	}
	addon := cat.AddonByProvider(r.Provider)
	if addon == nil || addon.Backend != plugin.BackendResource {
		return nil, fmt.Errorf("cannot restore %s: no installed plugin provisions %s resources", r.Name, r.Provider)
	}
	rtCat, _ := databaseRuntimeCatalog(client)
	raw, err := addon.ProvisionConfig(plugin.CreateInput{}, rtCat)
	if err != nil {
		return nil, fmt.Errorf("error building the %s config to restore %s: %s", addon.Type, r.Name, err)
	}
	msg := json.RawMessage(raw)
	res, err := client.ProvisionResource(&ct.ResourceReq{ProviderID: addon.Provider, Apps: []string{app.ID}, Config: &msg})
	if err != nil {
		return nil, fmt.Errorf("error provisioning a %s resource to restore %s: %s", addon.Type, r.Name, err)
	}
	newName := resourceDisplayName(res)
	merged := resname.MergeAttachment(releaseEnv, res.Env, "", true)
	attach := make(map[string]*string, len(merged))
	for k, v := range merged {
		s := v
		attach[k] = &s
	}
	if err := recordAddonAttachment(client, addon, res, attach); err != nil {
		return nil, fmt.Errorf("error recording the %s attachment: %s", newName, err)
	}

	spec := pluginCLIForResource(cat, res)
	restore := pluginAction(spec, "restore")
	if restore == nil || restore.StdinFile == "" {
		return nil, fmt.Errorf("cannot restore %s into %s: the %s plugin has no restore command that reads a dump", r.Name, newName, r.Provider)
	}
	exportLogf("Restoring %s into the new %s resource %s.", r.Name, addon.Type, newName)
	deadline := time.Now().Add(importRestoreTimeout)
	for attempt := 1; ; attempt++ {
		if _, err := dump.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("error seeking the %s dump: %s", r.Name, err)
		}
		err := runPluginRestore(client, spec, restore, newName, dump)
		if err == nil {
			return merged, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("error restoring %s into %s after %d attempts: %s", r.Name, newName, attempt, err)
		}
		if attempt == 1 {
			exportLogf("Waiting for %s to start before restoring (%s).", newName, err)
		}
		time.Sleep(importRestoreRetryDelay)
	}
}

func runPluginRestore(client controller.Client, spec *plugin.CLI, action *plugin.CLIAction, name string, dump io.Reader) error {
	config, err := pluginJobConfig(client, spec, action, pluginJobArgs(name))
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	config.ReleaseEnv = action.ReleaseEnv
	config.Data = action.Data
	config.Exit = false
	config.Stdin = dump
	config.Stdout = ioutil.Discard
	config.Stderr = &stderr
	if err := exportRunJob(client, *config); err != nil {
		return fmt.Errorf("%s%s", err, stderrDetail(&stderr))
	}
	return nil
}

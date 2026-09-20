package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	DefaultInstalledFile = "/etc/flynn/installed-plugins.json"
	EnvInstalledFile     = "FLYNN_INSTALLED_PLUGINS"
)

// Installed is the backup/restore inventory of one plugin. It is name-agnostic:
// any flynn-plugin=true app is listed so restore knows what was present.
// Capabilities (aliases, sirenia, cluster dump/restore) are stamped from
// flynn-plugin.json at install so Flynn core never hardcodes plugin names.
type Installed struct {
	Name            string         `json:"name"`
	Kind            string         `json:"kind,omitempty"`
	Source          string         `json:"source,omitempty"`
	Ref             string         `json:"ref,omitempty"`
	Wait            string         `json:"wait,omitempty"`
	AppID           string         `json:"app_id,omitempty"`
	CLI             *CLI           `json:"cli,omitempty"`
	Aliases         []string       `json:"aliases,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	GitHubRepo      string         `json:"github_repo,omitempty"`
	Datastore       bool           `json:"datastore,omitempty"`
	Sirenia         bool           `json:"sirenia,omitempty"`
	SireniaOptional bool           `json:"sirenia_optional,omitempty"`
	Backup          *BackupSpec    `json:"backup,omitempty"`
	Restore         *RestoreSpec   `json:"restore,omitempty"`
	Status          *StatusSpec    `json:"status,omitempty"`
	Dashboard       *DashboardSpec `json:"dashboard,omitempty"`
}

// BackupSpec is a cluster-backup dump produced by a plugin appliance.
type BackupSpec struct {
	File         string   `json:"file"`
	Process      string   `json:"process,omitempty"`
	Args         []string `json:"args,omitempty"`
	Env          []string `json:"env,omitempty"`
	RequireScale bool     `json:"require_scale,omitempty"`
}

// RestoreSpec loads a cluster-backup dump into a plugin appliance.
type RestoreSpec struct {
	File    string   `json:"file"`
	Sirenia bool     `json:"sirenia,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// StatusSpec is an optional cluster-status probe for a plugin leader.
type StatusSpec struct {
	Port     string `json:"port,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

func InstalledFile() string {
	if v := strings.TrimSpace(os.Getenv(EnvInstalledFile)); v != "" {
		return v
	}
	return DefaultInstalledFile
}

// ListInstalled returns plugin apps from a controller AppList. Cluster backup
// writes this as plugins.json; postgres already stores the apps/artifacts/
// blobstore objects, so restore does not run plugin install again.
func ListInstalled(apps []*ct.App) []Installed {
	out := []Installed{}
	for _, app := range apps {
		if app == nil || !app.Plugin() {
			continue
		}
		out = append(out, RecordFromApp(app))
	}
	return out
}

// RecordFromApp rebuilds the install record from app metadata. Missing
// flynn-plugin-record (plugins installed before this field existed) still
// yields name/kind/cli plus sirenia/datastore inferred from the app.
func RecordFromApp(app *ct.App) Installed {
	rec := Installed{
		Name:   app.Name,
		AppID:  app.ID,
		Kind:   app.Meta[MetaPluginKind],
		Source: app.Meta[MetaPluginSource],
		Ref:    app.Meta[MetaPluginRef],
		Wait:   app.Meta[MetaPluginWait],
		CLI:    CLIFromApp(app),
	}
	if raw := app.Meta[MetaPluginRecord]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &rec)
		rec.Name = app.Name
		rec.AppID = app.ID
		if rec.Source == "" {
			rec.Source = app.Meta[MetaPluginSource]
		}
		if rec.Ref == "" {
			rec.Ref = app.Meta[MetaPluginRef]
		}
		if rec.Wait == "" {
			rec.Wait = app.Meta[MetaPluginWait]
		}
		if rec.Kind == "" {
			rec.Kind = app.Meta[MetaPluginKind]
		}
		if rec.CLI == nil {
			rec.CLI = CLIFromApp(app)
		}
		if rec.Dashboard == nil {
			rec.Dashboard = DashboardFromApp(app)
		}
	}
	if app.Meta[MetaDatastore] == "true" {
		rec.Datastore = true
	}
	if app.Strategy == "sirenia" {
		rec.Sirenia = true
	}
	return rec
}

// ResolveNames are the install aliases this plugin answers to.
func (p Installed) ResolveNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(p.Name)
	add(p.Provider)
	if p.CLI != nil {
		add(p.CLI.Command)
	}
	for _, a := range p.Aliases {
		add(a)
	}
	return out
}

func (p Installed) MatchesName(name string) bool {
	for _, n := range p.ResolveNames() {
		if n == name {
			return true
		}
	}
	return false
}

// IsSireniaManaged is true for core postgres and plugin apps that run sirenia.
func IsSireniaManaged(app *ct.App) bool {
	if app == nil {
		return false
	}
	if app.Name == "postgres" {
		return true
	}
	if !app.Plugin() {
		return false
	}
	if app.Strategy == "sirenia" {
		return true
	}
	return RecordFromApp(app).Sirenia
}

// SireniaServiceNames is postgres plus installed sirenia plugins. Hosts without
// an inventory file only wait on postgres (the required core appliance).
func SireniaServiceNames() []string {
	return SireniaServiceNamesFrom(ReadInstalled(""))
}

func SireniaServiceNamesFrom(installed []Installed) []string {
	names := []string{"postgres"}
	seen := map[string]struct{}{"postgres": {}}
	for _, p := range installed {
		if !p.Sirenia || p.Name == "" {
			continue
		}
		if _, ok := seen[p.Name]; ok {
			continue
		}
		seen[p.Name] = struct{}{}
		names = append(names, p.Name)
	}
	return names
}

// DatastoreService reports whether user jobs may resolve leader.<name>.discoverd.
func DatastoreService(name string) bool {
	if name == "postgres" {
		return true
	}
	for _, p := range ReadInstalled("") {
		if p.Datastore && p.MatchesName(name) {
			return true
		}
	}
	return false
}

// OptionalSirenia is true when a sirenia plugin boots with zero data peers.
func OptionalSirenia(name string) bool {
	if name == "postgres" {
		return false
	}
	for _, p := range ReadInstalled("") {
		if p.MatchesName(name) {
			return p.SireniaOptional
		}
	}
	// Unknown service: do not block upgrades waiting for a scaled-to-zero plugin.
	return true
}

func ReadInstalled(path string) []Installed {
	if path == "" {
		path = InstalledFile()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []Installed{}
	}
	var out []Installed
	if err := json.Unmarshal(data, &out); err != nil {
		var wrap struct {
			Plugins []Installed `json:"plugins"`
		}
		if err := json.Unmarshal(data, &wrap); err != nil {
			return []Installed{}
		}
		out = wrap.Plugins
	}
	if out == nil {
		return []Installed{}
	}
	return out
}

func WriteInstalled(path string, plugins []Installed) error {
	if path == "" {
		path = InstalledFile()
	}
	if plugins == nil {
		plugins = []Installed{}
	}
	data, err := json.MarshalIndent(plugins, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// BackupSpecFor returns the cluster dump spec, inferring mysqldump/mongodump
// from release env when older installs did not stamp a backup block.
func BackupSpecFor(p Installed, formation *ct.ExpandedFormation) *BackupSpec {
	if p.Backup != nil && p.Backup.File != "" && len(p.Backup.Args) > 0 {
		return p.Backup
	}
	if formation == nil || formation.Release == nil {
		return nil
	}
	env := formation.Release.Env
	if env["MYSQL_PWD"] != "" {
		return &BackupSpec{
			File:         "mysql.sql.gz",
			Process:      firstNonEmpty(p.BackupProcessHint(), p.Name),
			Args:         []string{"bash", "-c", "set -o pipefail; /usr/bin/mysqldump -h $MYSQL_HOST -u $MYSQL_USER --all-databases --flush-privileges | gzip -9"},
			Env:          []string{"MYSQL_HOST", "MYSQL_USER", "MYSQL_PWD"},
			RequireScale: true,
		}
	}
	if env["MONGO_PWD"] != "" {
		return &BackupSpec{
			File:         "mongodb.archive.gz",
			Process:      firstNonEmpty(p.BackupProcessHint(), p.Name),
			Args:         []string{"bash", "-c", "set -o pipefail; /usr/bin/mongodump --host $MONGO_HOST -u $MONGO_USER -p $MONGO_PWD --authenticationDatabase admin --archive | gzip -9"},
			Env:          []string{"MONGO_HOST", "MONGO_USER", "MONGO_PWD"},
			RequireScale: true,
		}
	}
	return nil
}

func (p Installed) BackupProcessHint() string {
	if p.Backup != nil && p.Backup.Process != "" {
		return p.Backup.Process
	}
	return ""
}

// RestoreSpecFor returns the cluster restore spec, inferring mysql/mongo load
// from release env when older backups omitted the restore block.
func RestoreSpecFor(p Installed, formation *ct.ExpandedFormation) *RestoreSpec {
	if p.Restore != nil && p.Restore.File != "" && len(p.Restore.Args) > 0 {
		return p.Restore
	}
	if formation == nil || formation.Release == nil {
		return nil
	}
	env := formation.Release.Env
	leader := "leader." + p.Name + ".discoverd"
	if env["MYSQL_PWD"] != "" {
		return &RestoreSpec{
			File:    "mysql.sql.gz",
			Sirenia: true,
			Args:    []string{"mysql", "-u", "flynn", "-h", leader},
			Env:     []string{"MYSQL_PWD"},
		}
	}
	if env["MONGO_PWD"] != "" {
		return &RestoreSpec{
			File:    "mongodb.archive.gz",
			Sirenia: true,
			Args:    []string{"mongorestore", "-h", leader, "-u", "flynn", "-p", "${app.MONGO_PWD}", "--archive"},
			Env:     []string{"MONGO_PWD"},
		}
	}
	return nil
}

func ExpandReleaseArgs(args []string, env map[string]string) ([]string, error) {
	return InterpolateAll(args, Interp{App: env})
}

func JobEnvFromSpec(keys []string, releaseEnv map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v := releaseEnv[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

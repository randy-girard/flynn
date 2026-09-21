package backup

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

type backupAppClient interface {
	GetApp(string) (*ct.App, error)
	GetAppRelease(string) (*ct.Release, error)
	GetFormation(string, string) (*ct.Formation, error)
	GetExpandedFormation(string, string) (*ct.ExpandedFormation, error)
	FormationList(string) ([]*ct.Formation, error)
}

func Run(client controller.Client, out io.Writer, progress ProgressBar) error {
	tw := NewTarWriter("flynn-backup-"+time.Now().UTC().Format("2006-01-02_150405"), out, progress)
	defer tw.Close()

	data, err := getApps(client)
	if err != nil {
		return err
	}

	apps, err := client.AppList()
	if err != nil {
		return fmt.Errorf("error listing apps for plugin inventory: %s", err)
	}
	installed := plugin.ListInstalled(apps)
	if err := includePluginFormations(client, data, installed); err != nil {
		return err
	}
	if err := tw.WriteJSON("flynn.json", data); err != nil {
		return err
	}
	if err := tw.WriteJSON("plugins.json", installed); err != nil {
		return fmt.Errorf("error writing plugin inventory: %s", err)
	}

	pgRelease := data["postgres"].Release
	pgJob := &ct.NewJob{
		ReleaseID: pgRelease.ID,
		Args:      []string{"bash", "-c", postgresDumpCommand()},
		Env: map[string]string{
			"PGHOST":     pgRelease.Env["PGHOST"],
			"PGUSER":     pgRelease.Env["PGUSER"],
			"PGPASSWORD": pgRelease.Env["PGPASSWORD"],
		},
		DisableLog: true,
		Partition:  ct.PartitionTypeBackground,
	}
	if err := tw.WriteCommandOutput(client, "postgres.sql.gz", "postgres", pgJob); err != nil {
		return fmt.Errorf("error dumping postgres database: %s", err)
	}

	for _, p := range installed {
		formation := data[p.Name]
		spec := plugin.BackupSpecFor(p, formation)
		if !shouldDumpPlugin(client, p, spec, formation) {
			continue
		}
		job := &ct.NewJob{
			ReleaseID:  formation.Release.ID,
			Args:       spec.Args,
			Env:        plugin.JobEnvFromSpec(spec.Env, formation.Release.Env),
			DisableLog: true,
			Partition:  ct.PartitionTypeBackground,
		}
		if err := tw.WriteCommandOutput(client, spec.File, p.Name, job); err != nil {
			return fmt.Errorf("error dumping %s database: %s", p.Name, err)
		}
	}
	return nil
}

// postgresDumpGzipLevel is gzip -N for pg_dumpall. Production backups stay at
// 9. Smoke sets FLYNN_BACKUP_GZIP_LEVEL=1 because the blobstore makes the
// dump multi-gigabyte and -9 dominates the backup step.
func postgresDumpGzipLevel() string {
	level := strings.TrimSpace(os.Getenv("FLYNN_BACKUP_GZIP_LEVEL"))
	if len(level) == 1 && level[0] >= '1' && level[0] <= '9' {
		return level
	}
	return "9"
}

func postgresDumpCommand() string {
	return fmt.Sprintf("set -o pipefail; pg_dumpall --clean --if-exists | gzip -%s", postgresDumpGzipLevel())
}

type jobLister interface {
	JobList(appID string) ([]*ct.Job, error)
}

// shouldDumpPlugin reports whether a plugin appliance should be dumped into the
// cluster backup. RequireScale skips idle appliances, but after an upgrade the
// desired formation can be 0 while a dump process is still up. In that case
// dump from the running job so restore still gets mysql/mongodb data.
func shouldDumpPlugin(client jobLister, p plugin.Installed, spec *plugin.BackupSpec, formation *ct.ExpandedFormation) bool {
	if spec == nil || formation == nil || formation.Release == nil {
		return false
	}
	if !spec.RequireScale {
		return true
	}
	proc := spec.Process
	if proc == "" {
		proc = p.Name
	}
	if formation.Processes[proc] > 0 {
		return true
	}
	jobs, err := client.JobList(p.Name)
	if err != nil {
		return false
	}
	for _, j := range jobs {
		if j == nil {
			continue
		}
		if j.Type == proc && (j.State == ct.JobStateUp || j.State == ct.JobStateStarting) {
			return true
		}
	}
	return false
}

func includePluginFormations(client backupAppClient, data map[string]*ct.ExpandedFormation, installed []plugin.Installed) error {
	for _, p := range installed {
		if data[p.Name] != nil {
			continue
		}
		app, err := client.GetApp(p.Name)
		if err != nil {
			continue
		}
		release, err := client.GetAppRelease(app.ID)
		if err != nil {
			return fmt.Errorf("error getting %s app release: %s", p.Name, err)
		}
		ef, err := client.GetExpandedFormation(app.ID, release.ID)
		if err != nil {
			return fmt.Errorf("error getting %s expanded formation: %s", p.Name, err)
		}
		if ef.DeprecatedImageArtifact == nil && len(ef.Artifacts) > 0 {
			ef.DeprecatedImageArtifact = ef.Artifacts[0]
		}
		data[p.Name] = ef
	}
	return nil
}

func getApps(client backupAppClient) (map[string]*ct.ExpandedFormation, error) {
	apps := map[string]bool{
		"postgres":   true,
		"discoverd":  true,
		"flannel":    true,
		"controller": true,
	}
	data := make(map[string]*ct.ExpandedFormation, len(apps))
	for name, required := range apps {
		app, err := client.GetApp(name)
		if err != nil {
			if required {
				return nil, fmt.Errorf("error getting %s app details: %s", name, err)
			}
			continue
		}
		release, err := client.GetAppRelease(app.ID)
		if err != nil {
			return nil, fmt.Errorf("error getting %s app release: %s", name, err)
		}
		formation, err := formationForBackup(client, app.ID, release.ID)
		if err != nil {
			return nil, fmt.Errorf("error getting %s app formation: %s", name, err)
		}
		data[name] = &ct.ExpandedFormation{
			App:       app,
			Release:   release,
			Processes: formation.Processes,

			DeprecatedImageArtifact: &ct.Artifact{Type: ct.DeprecatedArtifactTypeDocker},
		}
	}
	return data, nil
}

func formationNotFound(err error) bool {
	if err == nil {
		return false
	}
	if err == ct.ErrNotFound {
		return true
	}
	return strings.Contains(err.Error(), "resource not found")
}

// formationForBackup returns the formation for the current release. After an
// updater deploy the current release can exist before its formation row, which
// used to abort flynn-host backup with "resource not found". Fall back to
// processes from another scaled formation on the same app, then an empty map.
func formationForBackup(client backupAppClient, appID, releaseID string) (*ct.Formation, error) {
	f, err := client.GetFormation(appID, releaseID)
	if err == nil && f != nil {
		return f, nil
	}
	if err != nil && !formationNotFound(err) {
		return nil, err
	}
	list, listErr := client.FormationList(appID)
	if listErr != nil && !formationNotFound(listErr) {
		return nil, listErr
	}
	processes := map[string]int{}
	for _, item := range list {
		if item == nil {
			continue
		}
		if item.ReleaseID == releaseID {
			return item, nil
		}
		for _, n := range item.Processes {
			if n > 0 {
				processes = item.Processes
				break
			}
		}
	}
	return &ct.Formation{AppID: appID, ReleaseID: releaseID, Processes: processes}, nil
}

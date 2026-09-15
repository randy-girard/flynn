package backup

import (
	"fmt"
	"io"
	"time"

	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/plugin"
)

type backupAppClient interface {
	GetApp(string) (*ct.App, error)
	GetAppRelease(string) (*ct.Release, error)
	GetFormation(string, string) (*ct.Formation, error)
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
		Args:      []string{"bash", "-c", "set -o pipefail; pg_dumpall --clean --if-exists | gzip -9"},
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
		if spec == nil || formation == nil || formation.Release == nil {
			continue
		}
		proc := spec.Process
		if proc == "" {
			proc = p.Name
		}
		if spec.RequireScale && formation.Processes[proc] <= 0 {
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
		formation, err := client.GetFormation(app.ID, release.ID)
		if err != nil {
			return fmt.Errorf("error getting %s app formation: %s", p.Name, err)
		}
		data[p.Name] = &ct.ExpandedFormation{
			App:                     app,
			Release:                 release,
			Processes:               formation.Processes,
			DeprecatedImageArtifact: &ct.Artifact{Type: ct.DeprecatedArtifactTypeDocker},
		}
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
		formation, err := client.GetFormation(app.ID, release.ID)
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

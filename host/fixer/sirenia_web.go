package fixer

import (
	"fmt"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/discoverd/client"
)

func desiredWebProcesses(hostCount int) int {
	if hostCount >= 2 {
		return 2
	}
	return 1
}

func sireniaAPIService(svc string) string {
	return svc + "-api"
}

// EnsureSireniaResourceAPIs scales web processes for postgres, mariadb, and
// mongodb so resource provisioning APIs are registered in discoverd.
func (f *ClusterFixer) EnsureSireniaResourceAPIs(c controller.Client) error {
	for _, svc := range sireniaDBApps {
		if err := f.EnsureSireniaWebAPI(c, svc); err != nil {
			f.l.Error("error ensuring sirenia resource API", "service", svc, "err", err)
		}
	}
	return nil
}

// EnsureSireniaWebAPI ensures the web process is running for a sirenia
// database app. The web process hosts *-api, which the controller uses for
// flynn resource add/remove.
func (f *ClusterFixer) EnsureSireniaWebAPI(c controller.Client, svc string) error {
	log := f.l.New("fn", "EnsureSireniaWebAPI", "service", svc)

	app, err := appByName(c, svc)
	if err != nil {
		return err
	}
	if app.Strategy != "sirenia" {
		return nil
	}

	want := desiredWebProcesses(len(f.hosts))
	apiSvc := discoverd.NewService(sireniaAPIService(svc))
	if instances, err := apiSvc.Instances(); err == nil && len(instances) >= want {
		formation, ferr := c.GetFormation(app.ID, app.ReleaseID)
		if ferr == nil && formation.Processes["web"] >= want {
			log.Info("resource API already available", "instances", len(instances), "web", formation.Processes["web"])
			return nil
		}
	}

	formation, err := c.GetFormation(app.ID, app.ReleaseID)
	if err != nil {
		return fmt.Errorf("get formation: %w", err)
	}
	if formation.Processes == nil {
		formation.Processes = make(map[string]int)
	}

	dbCount := formation.Processes[svc]
	if dbCount == 0 {
		dbCount = desiredDBPeers(len(f.hosts), formation, svc)
	}
	formation.Processes[svc] = dbCount
	formation.Processes["web"] = want

	log.Info("scaling sirenia web process for resource API", "web", want, "db_peers", dbCount)
	if err := c.PutFormation(formation); err != nil {
		return err
	}
	return f.waitForSireniaWebAPI(c, app, svc, want, 5*time.Minute)
}

func (f *ClusterFixer) waitForSireniaWebAPI(c controller.Client, app *ct.App, svc string, want int, timeout time.Duration) error {
	log := f.l.New("fn", "waitForSireniaWebAPI", "service", svc, "want", want)
	apiSvc := sireniaAPIService(svc)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jobs, err := c.JobList(app.ID)
		if err != nil {
			return err
		}
		webUp := 0
		for _, job := range jobs {
			if job.ReleaseID != app.ReleaseID || job.Type != "web" {
				continue
			}
			if job.State == ct.JobStateUp {
				webUp++
			}
		}
		instances, _ := discoverd.NewService(apiSvc).Instances()
		log.Info("web API status", "web_up", webUp, "want", want, "api_instances", len(instances))
		if webUp >= want && len(instances) >= want {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s web=%d (%s in discoverd)", svc, want, apiSvc)
}

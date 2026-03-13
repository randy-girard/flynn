package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/discoverd/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/flynn/flynn/pkg/status"
	"github.com/flynn/flynn/pkg/version"
	"github.com/flynn/flynn/updater/types"
	"github.com/mattn/go-colorable"
	"github.com/inconshreveable/log15"
)

var redisImage, slugBuilder, slugRunner *ct.Artifact

// use a flag to determine whether to use a TTY log formatter because actually
// assigning a TTY to the job causes reading images via stdin to fail.
var isTTY = flag.Bool("tty", false, "use a TTY log formatter")

const deployTimeout = 30 * time.Minute

func main() {
	flag.Parse()
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	log := log15.New()
	if *isTTY {
		log.SetHandler(log15.StreamHandler(colorable.NewColorableStdout(), log15.TerminalFormat()))
	}

	var images map[string]*ct.Artifact
	if err := json.NewDecoder(os.Stdin).Decode(&images); err != nil {
		log.Error("error decoding images", "err", err)
		return err
	}

	req, err := http.NewRequest("GET", "http://status-web.discoverd", nil)
	if err != nil {
		return err
	}
	req.Header = make(http.Header)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error("error getting cluster status", "err", err)
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Error("cluster status is unhealthy", "code", res.StatusCode)
		return fmt.Errorf("cluster is unhealthy")
	}
	var statusWrapper struct {
		Data struct {
			Detail map[string]status.Status
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&statusWrapper); err != nil {
		log.Error("error decoding cluster status JSON", "err", err)
		return err
	}
	statuses := statusWrapper.Data.Detail

	instances, err := discoverd.GetInstances("controller", 10*time.Second)
	if err != nil {
		log.Error("error looking up controller in service discovery", "err", err)
		return err
	}
	client, err := controller.NewClient("", instances[0].Meta["AUTH_KEY"])
	if err != nil {
		log.Error("error creating controller client", "err", err)
		return err
	}

	log.Info("validating images")
	for _, app := range updater.SystemApps {
		if v := version.Parse(statuses[app.Name].Version); !v.Dev && app.MinVersion != "" && v.Before(version.Parse(app.MinVersion)) {
			log.Info(
				"not updating image of system app, can't upgrade from running version",
				"app", app.Name,
				"version", v,
			)
			continue
		}
		if _, ok := images[app.Name]; !ok {
			err := fmt.Errorf("missing image: %s", app.Name)
			log.Error(err.Error())
			return err
		}
	}

	// Repair any sirenia clusters with deposed peers BEFORE creating
	// image artifacts.  CreateArtifact depends on blobstore which
	// depends on postgres being healthy (with asyncs).
	repairSireniaClusters(log)

	log.Info("creating new image artifacts")
	createArtifactWithRetry := func(name string, img *ct.Artifact) error {
		for attempt := 1; attempt <= 6; attempt++ {
			if err := client.CreateArtifact(img); err != nil {
				log.Warn("error creating image artifact, retrying",
					"name", name, "attempt", attempt, "err", err)
				time.Sleep(10 * time.Second)
				continue
			}
			return nil
		}
		return fmt.Errorf("failed to create %s image artifact after retries", name)
	}
	redisImage = images["redis"]
	if err := createArtifactWithRetry("redis", redisImage); err != nil {
		log.Error(err.Error())
		return err
	}
	slugRunner = images["slugrunner"]
	if err := createArtifactWithRetry("slugrunner", slugRunner); err != nil {
		log.Error(err.Error())
		return err
	}
	slugBuilder = images["slugbuilder"]
	if err := createArtifactWithRetry("slugbuilder", slugBuilder); err != nil {
		log.Error(err.Error())
		return err
	}

	// deploy system apps in order first
	for _, appInfo := range updater.SystemApps {
		if appInfo.ImageOnly {
			continue // skip ImageOnly updates
		}
		// Skip discoverd and flannel — their lifecycle is managed by the
		// host daemon's resurrection logic.  Redeploying them through the
		// controller uses an all-at-once strategy that kills every instance
		// simultaneously, which takes down DNS and overlay networking
		// cluster-wide and causes cascading failures in all other services.
		if appInfo.Name == "discoverd" || appInfo.Name == "flannel" {
			log.Info("skipping deploy of infrastructure app (managed by host daemon)", "name", appInfo.Name)
			continue
		}
		log := log.New("name", appInfo.Name)
		log.Info("starting deploy of system app")

		app, err := client.GetApp(appInfo.Name)
		if err == controller.ErrNotFound && appInfo.Optional {
			log.Info(
				"skipped deploy of system app",
				"reason", "optional app not present",
				"app", appInfo.Name,
			)
			continue
		} else if err != nil {
			log.Error("error getting app", "err", err)
			return err
		}
		var deployErr error
		for attempt := 1; ; attempt++ {
			deployErr = deployApp(client, app, images[appInfo.Name], appInfo.UpdateRelease, log)
			if deployErr == nil {
				break
			}
			if e, ok := deployErr.(errDeploySkipped); ok {
				log.Info(
					"skipped deploy of system app",
					"reason", e.reason,
					"app", appInfo.Name,
				)
				deployErr = nil
				break
			}
			// Sirenia-based apps (postgres, mariadb, mongodb) may not have
			// fully reformed their cluster yet after a daemon restart.
			// Retry for up to 2 minutes to give asyncs time to rejoin.
			if strings.Contains(deployErr.Error(), "sirenia") && attempt < 12 {
				log.Warn("sirenia cluster not ready, retrying deploy",
					"app", appInfo.Name, "err", deployErr, "attempt", attempt)
				time.Sleep(10 * time.Second)
				continue
			}
			return deployErr
		}
		if deployErr != nil {
			continue
		}
		log.Info("finished deploy of system app")
	}

	// deploy all other apps (including provisioned Redis apps)
	apps, err := client.AppList()
	if err != nil {
		log.Error("error getting apps", "err", err)
		return err
	}
	for _, app := range apps {
		log := log.New("name", app.Name)

		if app.RedisAppliance() {
			log.Info("starting deploy of Redis app")
			if err := deployApp(client, app, redisImage, nil, log); err != nil {
				if e, ok := err.(errDeploySkipped); ok {
					log.Info("skipped deploy of Redis app", "reason", e.reason)
					continue
				}
				return err
			}
			log.Info("finished deploy of Redis app")
			continue
		}

		if app.System() {
			continue
		}

		log.Info("starting deploy of app to update slugrunner")
		if err := deployApp(client, app, slugRunner, nil, log); err != nil {
			if e, ok := err.(errDeploySkipped); ok {
				log.Info("skipped deploy of app", "reason", e.reason)
				continue
			}
			return err
		}
		log.Info("finished deploy of app")
	}
	return nil
}

type errDeploySkipped struct {
	reason string
}

func (e errDeploySkipped) Error() string {
	return e.reason
}

func deployApp(client controller.Client, app *ct.App, image *ct.Artifact, updateFn updater.UpdateReleaseFn, log log15.Logger) error {
	release, err := client.GetAppRelease(app.ID)
	if err != nil {
		log.Error("error getting release", "err", err)
		return err
	}
	if len(release.ArtifactIDs) == 0 {
		return errDeploySkipped{"release has no artifacts"}
	}
	artifact, err := client.GetArtifact(release.ArtifactIDs[0])
	if err != nil {
		log.Error("error getting release artifact", "err", err)
		return err
	}
	if !app.System() && release.IsGitDeploy() {
		if artifact.Meta["flynn.component"] != "slugrunner" {
			return errDeploySkipped{"app not using slugrunner image"}
		}
	}
	skipDeploy := artifact.Manifest().ID() == image.Manifest().ID()
	if updateImageIDs(release.Env) {
		skipDeploy = false
	}
	if skipDeploy {
		return errDeploySkipped{"app is already using latest images"}
	}
	if err := client.CreateArtifact(image); err != nil {
		log.Error("error creating artifact", "err", err)
		return err
	}
	release.ID = ""
	release.ArtifactIDs[0] = image.ID
	if updateFn != nil {
		updateFn(release)
	}
	if err := client.CreateRelease(app.ID, release); err != nil {
		log.Error("error creating new release", "err", err)
		return err
	}
	timeoutCh := make(chan struct{})
	time.AfterFunc(deployTimeout, func() { close(timeoutCh) })
	if err := client.DeployAppRelease(app.ID, release.ID, timeoutCh); err != nil {
		log.Error("error deploying app", "err", err)
		return err
	}
	return nil
}

// updateImageIDs updates REDIS_IMAGE_ID, SLUGBUILDER_IMAGE_ID and
// SLUGRUNNER_IMAGE_ID if they are set and have an old ID, and also
// replaces the legacy REDIS_IMAGE_URI, SLUGBUILDER_IMAGE_URI and
// SLUGRUNNER_IMAGE_URI
func updateImageIDs(env map[string]string) bool {
	updated := false
	for prefix, newID := range map[string]string{
		"REDIS":       redisImage.ID,
		"SLUGBUILDER": slugBuilder.ID,
		"SLUGRUNNER":  slugRunner.ID,
	} {
		idKey := prefix + "_IMAGE_ID"
		if id, ok := env[idKey]; ok && id != newID {
			env[idKey] = newID
			updated = true
		}

		uriKey := prefix + "_IMAGE_URI"
		if _, ok := env[uriKey]; ok {
			delete(env, uriKey)
			env[idKey] = newID
			updated = true
		}
	}
	return updated
}


// repairSireniaClusters clears deposed peers from sirenia-managed services.
// After a daemon restart the old primary may have been deposed by a sync
// takeover; the deposed peer never automatically rejoins, leaving the cluster
// without asyncs.  Clearing the Deposed list lets the primary re-add them.
func repairSireniaClusters(log log15.Logger) {
	appliances := []string{"postgres", "mariadb", "mongodb"}
	for _, svc := range appliances {
		svcLog := log.New("service", svc)
		service := discoverd.NewService(svc)

		meta, err := service.GetMeta()
		if err != nil {
			continue
		}

		var state sirenia.State
		if err := json.Unmarshal(meta.Data, &state); err != nil {
			svcLog.Warn("failed to decode sirenia state", "err", err)
			continue
		}

		if len(state.Deposed) == 0 {
			continue
		}

		svcLog.Info("clearing deposed peers from sirenia cluster",
			"deposed_count", len(state.Deposed))

		state.Deposed = nil

		data, err := json.Marshal(&state)
		if err != nil {
			svcLog.Error("failed to encode repaired sirenia state", "err", err)
			continue
		}
		meta.Data = data
		if err := service.SetMeta(meta); err != nil {
			svcLog.Error("failed to write repaired sirenia state", "err", err)
			continue
		}

		svcLog.Info("cleared deposed peers, waiting for cluster to reform")
		time.Sleep(10 * time.Second)
	}
}
package app_garbage_collection

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

type context struct {
	db           *postgres.DB
	client       controller.Client
	logger       log15.Logger
	artifactRepo *data.ArtifactRepo
	releaseRepo  *data.ReleaseRepo
}

func JobHandler(db *postgres.DB, client controller.Client, logger log15.Logger) func(*que.Job) error {
	artifacts := data.NewArtifactRepo(db)
	releases := data.NewReleaseRepo(db, artifacts, que.NewClient(db.ConnPool))
	return (&context{db, client, logger, artifacts, releases}).HandleAppGarbageCollection
}

func (c *context) HandleAppGarbageCollection(job *que.Job) (err error) {
	log := c.logger.New("fn", "HandleAppGarbageCollection")
	log.Info("handling garbage collection", "job_id", job.ID, "error_count", job.ErrorCount)

	var gc ct.AppGarbageCollection
	if err := json.Unmarshal(job.Args, &gc); err != nil {
		log.Error("error unmarshaling job", "err", err)
		return err
	}

	log = log.New("app.id", gc.AppID)
	defer func() {
		if err := c.createEvent(&gc, err); err != nil {
			log.Error("error creating garbage collection event", "err", err)
		}
		log.Info("garbage collection finished")
	}()

	log.Info("getting app")
	app, err := c.client.GetApp(gc.AppID)
	if err != nil {
		log.Error("error getting app", "err", err)
		return err
	}

	keep := ct.DefaultBlobGCKeep
	var maxAge time.Duration
	if s, err := data.NewRuntimeProfileRepo(c.db).Settings(); err == nil {
		keep = s.BlobGCKeepOrDefault()
		if age := strings.TrimSpace(s.BlobGCMaxAge); age != "" && age != "0" {
			if d, err := time.ParseDuration(age); err == nil {
				maxAge = d
			}
		}
	}
	meta, ok := app.Meta[ct.MetaGCMaxInactiveSlugReleases]
	if ok && meta == "false" {
		log.Info("skipping blob GC because gc.max_inactive_slug_releases=false")
		return nil
	}
	if ok {
		if n, err := strconv.Atoi(meta); err == nil && n >= 0 {
			keep = n
		}
	}
	log.Info(fmt.Sprintf("blob GC keep=%d max_age=%s", keep, maxAge))

	log.Info("getting app releases")
	releases, err := c.client.AppReleaseList(app.ID)
	if err != nil {
		log.Error("error getting app releases", "err", err)
		return err
	}
	log.Info("getting app formations")
	formations, err := c.client.FormationList(app.ID)
	if err != nil {
		log.Error("error getting app formations", "err", err)
		return err
	}

	ids := make([]string, 0)
	for _, rel := range releases {
		ids = append(ids, rel.ArtifactIDs...)
	}
	arts, err := c.artifactRepo.ListIDs(ids...)
	if err != nil {
		log.Error("error listing artifacts", "err", err)
		return err
	}

	reapIDs := SelectReapReleaseIDs(releases, formations, keep, maxAge, time.Now(), func(rel *ct.Release) (string, bool) {
		return BlobstoreAppURI(rel, arts)
	})
	if len(reapIDs) == 0 {
		log.Info("no old blobs to reap")
		return nil
	}

	relByID := make(map[string]*ct.Release, len(releases))
	for _, rel := range releases {
		relByID[rel.ID] = rel
	}
	log.Info(fmt.Sprintf("reaping %d old release blobs", len(reapIDs)))
	gc.DeletedReleases = make([]string, 0, len(reapIDs))
	for _, id := range reapIDs {
		rel := relByID[id]
		if rel == nil {
			continue
		}
		log.Info("reaping release blob", "release.id", rel.ID)
		if err := c.releaseRepo.ReapBlobs(app, rel); err != nil {
			log.Error("error reaping release blob", "release.id", rel.ID, "err", err)
			continue
		}
		gc.DeletedReleases = append(gc.DeletedReleases, rel.ID)
	}

	return nil
}

func (c *context) createEvent(gc *ct.AppGarbageCollection, err error) error {
	e := ct.AppGarbageCollectionEvent{AppGarbageCollection: gc}
	if err != nil {
		e.Error = err.Error()
	}
	return c.db.Exec("event_insert", gc.AppID, gc.AppID, string(ct.EventTypeAppGarbageCollection), e)
}

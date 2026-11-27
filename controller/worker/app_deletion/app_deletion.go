package app_deletion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/datastoreguard"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// blobstoreTimeout bounds each app cache DELETE so a stalled blobstore fails
// the attempt (and que retries it) instead of holding a worker forever.
const blobstoreTimeout = 30 * time.Second

type context struct {
	db           *postgres.DB
	client       controller.Client
	logger       log15.Logger
	http         *http.Client
	blobstoreURL string
}

func newContext(db *postgres.DB, client controller.Client, logger log15.Logger) *context {
	return &context{
		db:           db,
		client:       client,
		logger:       logger,
		http:         &http.Client{Timeout: blobstoreTimeout},
		blobstoreURL: "http://blobstore.discoverd",
	}
}

func JobHandler(db *postgres.DB, client controller.Client, logger log15.Logger) func(*que.Job) error {
	return newContext(db, client, logger).HandleAppDeletion
}

// HandleAppDeletion deletes an app. The steps that can fail and that call
// other services come first, and what cannot be rebuilt goes last:
//
//  1. deprovision the app's own resources (provider calls)
//  2. delete the app's git repo and build cache from the blobstore
//  3. delete the app's routes
//  4. unset the current release and delete every release (their slugs go
//     through release_cleanup)
//  5. delete the app row, formations and resource attachments, and
//     decommission the app's volumes (the scheduler destroys them once no
//     job holds them; ct.AppMetaKeepVolumes keeps them)
//
// A failed step leaves the later ones untouched: the app keeps its routes,
// releases and current release while a provider or the blobstore is down.
// Each step lists what is left, so a retry only does the remaining work.
// Every failed attempt writes an app_deletion event with the error and puts
// the error on the app (ct.AppMetaDeletionError, shown by `flynn apps:info`),
// and que retries the job.
func (c *context) HandleAppDeletion(job *que.Job) (err error) {
	log := c.logger.New("fn", "HandleAppDeletion")
	log.Info("handling app deletion", "job_id", job.ID, "error_count", job.ErrorCount)

	var app ct.App
	if err := json.Unmarshal(job.Args, &app); err != nil {
		log.Error("error unmarshaling job", "err", err)
		return err
	}
	log = log.New("app_id", app.ID)

	ref := app.ID
	if ref == "" {
		ref = app.Name
	}
	got, getErr := c.client.GetApp(ref)
	if getErr == controller.ErrNotFound {
		// An earlier job (a second DELETE, or an attempt whose commit
		// landed before the worker stopped) already deleted it.
		log.Info("app already deleted")
		return nil
	}
	if getErr != nil {
		log.Error("error loading app before deletion", "err", getErr)
		return getErr
	}
	name := strings.TrimSpace(app.Name)
	keepVolumes := false
	if got != nil {
		keepVolumes = got.Meta[ct.AppMetaKeepVolumes] == "true"
		if app.ID == "" {
			app.ID = got.ID
		}
		if name == "" {
			name = strings.TrimSpace(got.Name)
		}
	}
	if err := datastoreguard.RefuseIfReferenced(data.NewDatastoreRefSource(c.db), name); err != nil {
		log.Error("refusing to delete referenced datastore", "err", err)
		return err
	}

	a := ct.AppDeletion{AppID: app.ID}
	defer func() {
		if err != nil {
			c.recordFailure(log, app.ID, err)
		}
		c.createEvent(&a, err)
	}()

	if err := c.deleteResources(log, &a); err != nil {
		return err
	}
	if err := c.deleteCache(log, app.ID); err != nil {
		return err
	}
	if err := c.deleteRoutes(log, &a); err != nil {
		return err
	}
	if err := c.deleteReleases(log, &a); err != nil {
		return err
	}

	log.Info("deleting app")
	tx, err := c.db.Begin()
	if err != nil {
		log.Error("error starting db transaction", "err", err)
		return err
	}
	err = tx.Exec("app_delete", app.ID)
	if err != nil {
		log.Error("error executing app deletion query", "err", err)
		tx.Rollback()
		return err
	}
	err = tx.Exec("formation_delete_by_app", app.ID)
	if err != nil {
		log.Error("error executing formation deletion query", "err", err)
		tx.Rollback()
		return err
	}
	err = tx.Exec("app_resource_delete_by_app", app.ID)
	if err != nil {
		log.Error("error executing resource deletion query", "err", err)
		tx.Rollback()
		return err
	}
	if err := c.decommissionVolumes(log, tx, &a, keepVolumes); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		a.DecommissionedVolumes, a.KeptVolumes = nil, nil
		return err
	}
	return nil
}

// decommissionVolumes hands the app's data volumes to the scheduler, which
// destroys each one on its host once no job holds it (NEW-35). It runs in
// the transaction that deletes the app row, so a failed earlier step leaves
// the volumes of a still-serving app alone. An app with
// ct.AppMetaKeepVolumes=true keeps them; they are listed as kept.
func (c *context) decommissionVolumes(log log15.Logger, tx *postgres.DBTx, a *ct.AppDeletion, keep bool) error {
	if keep {
		vols, err := data.NewVolumeRepo(c.db).AppList(a.AppID)
		if err != nil {
			log.Error("error listing app volumes", "err", err)
			return err
		}
		for _, vol := range vols {
			if vol.State == ct.VolumeStateDestroyed || vol.DecommissionedAt != nil {
				continue
			}
			log.Warn("keeping volume of deleted app ("+ct.AppMetaKeepVolumes+")", "volume_id", vol.ID, "host_id", vol.HostID)
			a.KeptVolumes = append(a.KeptVolumes, vol)
		}
		return nil
	}
	vols, err := data.DecommissionAppVolumes(tx, a.AppID)
	if err != nil {
		log.Error("error decommissioning app volumes", "err", err)
		return err
	}
	for _, vol := range vols {
		log.Info("decommissioned volume of deleted app", "volume_id", vol.ID, "host_id", vol.HostID, "job_type", vol.JobType)
	}
	a.DecommissionedVolumes = vols
	return nil
}

// followersFirst orders resources that follow a leader (a FLYNN_<ENGINE>_LEADER
// key) before the others. A datastore plugin refuses to remove a primary
// while its follower exists, so an app with both must deprovision the
// follower first (CORE-57). The order is otherwise kept.
func followersFirst(resources []*ct.Resource) []*ct.Resource {
	follows := func(r *ct.Resource) bool {
		if r == nil {
			return false
		}
		for k, v := range r.Env {
			if strings.HasPrefix(k, "FLYNN_") && strings.HasSuffix(k, "_LEADER") && strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	out := make([]*ct.Resource, 0, len(resources))
	for _, r := range resources {
		if follows(r) {
			out = append(out, r)
		}
	}
	for _, r := range resources {
		if !follows(r) {
			out = append(out, r)
		}
	}
	return out
}

// deleteResources deprovisions the resources only this app uses. A resource
// another app is attached to stays; the app row deletion detaches it.
func (c *context) deleteResources(log log15.Logger, a *ct.AppDeletion) error {
	log.Info("getting app resources")
	resources, err := c.client.AppResourceList(a.AppID)
	if err != nil {
		log.Error("error getting app resources", "err", err)
		return err
	}
	resources = followersFirst(resources)
	for _, resource := range resources {
		if len(resource.Apps) > 1 {
			continue
		}
		log.Info("deleting resource", "provider_id", resource.ProviderID, "resource_id", resource.ID)
		if _, err := c.client.DeleteResource(resource.ProviderID, resource.ID); err != nil {
			if err == controller.ErrNotFound {
				// removed since the list (another delete, or an
				// earlier attempt): nothing left to do for it
				log.Info("resource already removed", "resource_id", resource.ID)
				continue
			}
			log.Error("error deleting resource", "provider_id", resource.ProviderID, "resource_id", resource.ID, "err", err)
			return err
		}
		a.DeletedResources = append(a.DeletedResources, resource)
	}
	log.Info(fmt.Sprintf("deleted %d resources", len(a.DeletedResources)))
	return nil
}

// deleteCache removes the app's git repo tarball and build cache. A 404 is
// success, so a retry that already removed them passes.
func (c *context) deleteCache(log log15.Logger, appID string) error {
	log.Info("cleaning app cache")
	// TODO: share URL construction with gitreceive / flynn-receive
	for _, cacheURL := range []string{
		fmt.Sprintf("%s/repos/%s.tar", c.blobstoreURL, appID),
		fmt.Sprintf("%s/%s-cache.tgz", c.blobstoreURL, appID),
	} {
		req, err := http.NewRequest("DELETE", cacheURL, nil)
		if err != nil {
			log.Error("error creating app cache delete request", "err", err)
			return err
		}
		blobstoreauth.ApplyIfBlobstore(req)
		res, err := c.http.Do(req)
		if err != nil {
			log.Error("error performing app cache delete request", "err", err)
			return err
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotFound {
			e := fmt.Sprintf("unexpected status code %d cleaning cache URL %s", res.StatusCode, cacheURL)
			log.Error(e)
			return errors.New(e)
		}
	}
	return nil
}

func (c *context) deleteRoutes(log log15.Logger, a *ct.AppDeletion) error {
	log.Info("getting app routes")
	routes, err := c.client.AppRouteList(a.AppID)
	if err != nil {
		log.Error("error getting app routes", "err", err)
		return err
	}
	for _, route := range routes {
		log.Info("deleting route", "route_id", route.FormattedID())
		if err := deleteAppRoute(c.client, a.AppID, route.FormattedID()); err != nil {
			if err == controller.ErrNotFound {
				continue
			}
			log.Info("error deleting route", "route_id", route.FormattedID(), "err", err)
			return err
		}
		a.DeletedRoutes = append(a.DeletedRoutes, route)
	}
	log.Info(fmt.Sprintf("deleted %d routes", len(a.DeletedRoutes)))
	return nil
}

func (c *context) deleteReleases(log log15.Logger, a *ct.AppDeletion) error {
	log.Info("getting app releases")
	releases, err := c.client.AppReleaseList(a.AppID)
	if err != nil {
		log.Error("error getting app releases", "err", err)
		return err
	}

	// unset the app release so it doesn't prevent deleting it
	log.Info("unsetting app release")
	if err := c.db.Exec("app_update_release", a.AppID, nil); err != nil {
		log.Error("error unsetting app release", "err", err)
		return err
	}

	for _, release := range releases {
		log.Info("deleting release", "release_id", release.ID)
		if _, err := c.client.DeleteRelease(a.AppID, release.ID); err != nil {
			if err == controller.ErrNotFound {
				continue
			}
			log.Error("error deleting release", "release_id", release.ID, "err", err)
			return err
		}
		a.DeletedReleases = append(a.DeletedReleases, release)
	}
	log.Info(fmt.Sprintf("deleted %d releases", len(a.DeletedReleases)))
	return nil
}

// recordFailure puts the error on the app so `flynn apps:info` shows that a
// delete is pending and why it failed. The app row is gone once a retry
// succeeds, so the key needs no clearing.
func (c *context) recordFailure(log log15.Logger, appID string, cause error) {
	if appID == "" {
		return
	}
	msg := fmt.Sprintf("%s: %s", time.Now().UTC().Format(time.RFC3339), cause)
	if err := c.db.Exec("app_set_deletion_error", appID, msg); err != nil {
		log.Error("error recording deletion failure on app", "err", err)
	}
}

func (c *context) createEvent(a *ct.AppDeletion, err error) error {
	e := ct.AppDeletionEvent{AppDeletion: a}
	if err != nil {
		e.Error = err.Error()
	}
	return c.db.Exec("event_insert", a.AppID, a.AppID, string(ct.EventTypeAppDeletion), e)
}

type includedRouteDeleter interface {
	DeleteRouteForAppDeletion(appID, routeID string) error
}

func deleteAppRoute(client controller.Client, appID, routeID string) error {
	if d, ok := client.(includedRouteDeleter); ok {
		return d.DeleteRouteForAppDeletion(appID, routeID)
	}
	return client.DeleteRoute(appID, routeID)
}

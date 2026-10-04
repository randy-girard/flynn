package main

import (
	"net/http"
	"os"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/worker/app_deletion"
	"github.com/randy-girard/flynn/controller/worker/app_garbage_collection"
	"github.com/randy-girard/flynn/controller/worker/deployment"
	"github.com/randy-girard/flynn/controller/worker/domain_migration"
	"github.com/randy-girard/flynn/controller/worker/release_cleanup"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/pkg/status"
)

// workerCount must stay at or below postgres.MaxPoolConnections. Each
// worker holds a pool connection while a job runs; extra lock-loopers
// then fail with "All connections in pool are busy" and starve deployments.
const workerCount = 4

var logger = log15.New("app", "worker")

func main() {
	log := logger.New("fn", "main")

	log.Info("creating controller client")
	client, err := controller.NewClient("", os.Getenv("AUTH_KEY"))
	if err != nil {
		log.Error("error creating controller client", "err", err)
		shutdown.Fatal(err)
	}

	log.Info("connecting to postgres")
	db := postgres.Wait(nil, data.PrepareStatements)

	shutdown.BeforeExit(func() { db.Close() })

	go func() {
		status.AddHandler(func() status.Status {
			_, err := db.ConnPool.Exec("ping")
			if err != nil {
				return status.Unhealthy
			}
			return status.Healthy
		})
		addr := ":" + os.Getenv("PORT")
		hb, err := discoverd.AddServiceAndRegister("controller-worker", addr)
		if err != nil {
			shutdown.Fatal(err)
		}
		shutdown.BeforeExit(func() { hb.Close() })
		shutdown.Fatal(http.ListenAndServe(addr, nil))
	}()

	workers := que.NewWorkerPool(
		que.NewClient(db.ConnPool),
		que.WorkMap{
			"deployment":             deployment.JobHandler(db, client, logger),
			"app_deletion":           app_deletion.JobHandler(db, client, logger),
			"domain_migration":       domain_migration.JobHandler(db, client, logger),
			"release_cleanup":        release_cleanup.JobHandler(db, client, logger),
			"app_garbage_collection": app_garbage_collection.JobHandler(db, client, logger),
		},
		workerCount,
	)
	workers.Interval = 5 * time.Second

	log.Info("starting workers", "count", workerCount, "interval", workers.Interval)
	workers.Start()
	shutdown.BeforeExit(func() { workers.Shutdown() })

	go scheduleDailyBlobGC(client, logger)

	select {} // block and keep running
}

func scheduleDailyBlobGC(client controller.Client, log log15.Logger) {
	run := func() {
		apps, err := client.AppList()
		if err != nil {
			log.Error("daily blob gc: list apps", "err", err)
			return
		}
		for _, app := range apps {
			if app == nil {
				continue
			}
			if err := client.ScheduleAppGarbageCollection(app.ID); err != nil {
				log.Error("daily blob gc: schedule", "app.id", app.ID, "err", err)
			}
			time.Sleep(2 * time.Second)
		}
	}
	// Do not enqueue every app on worker boot. Cluster updates restart
	// controller-worker and would stampede postgres / starve deployments.
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		run()
	}
}

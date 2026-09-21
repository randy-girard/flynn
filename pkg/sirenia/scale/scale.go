package scale

import (
	"errors"
	"net/http"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/pkg/dialer"
	sirenia "github.com/randy-girard/flynn/pkg/sirenia/client"
)

// ScaleUp scales up a dormant Sirenia cluster
func ScaleUp(app, controllerKey, serviceAddr, procName, singleton string, logger log15.Logger) error {
	logger = logger.New("fn", "ScaleUp")

	// use an explicit HTTP client which doesn't use a retry dialer so we
	// don't retry dialling the cluster if it isn't scaled up
	httpClient := &http.Client{Transport: &http.Transport{Dial: dialer.Default.Dial}}
	sc := sirenia.NewClientWithHTTP(serviceAddr, httpClient)

	logger.Info("checking status", "host", serviceAddr)
	if status, err := sc.Status(); err == nil && status.Database != nil && status.Database.ReadWrite {
		logger.Info("database is up, skipping scale")
		// Skip the rest, the database is already available
		return nil
	} else if err != nil {
		logger.Info("error checking status", "err", err)
	} else {
		logger.Info("got status, but database is not read-write")
	}

	// Connect to controller.
	logger.Info("connecting to controller")
	client, err := controller.NewClient("", controllerKey)
	if err != nil {
		logger.Error("controller client error", "err", err)
		return err
	}

	// Retrieve the app release.
	logger.Info("retrieving app release", "app", app)
	release, err := client.GetAppRelease(app)
	if err == controller.ErrNotFound {
		logger.Error("release not found", "app", app)
		return errors.New("release not found")
	} else if err != nil {
		logger.Error("get release error", "app", app, "err", err)
		return err
	}

	// Retrieve current formation.
	logger.Info("retrieving formation", "app", app, "release_id", release.ID)
	formation, err := client.GetFormation(app, release.ID)
	if err == controller.ErrNotFound {
		logger.Error("formation not found", "app", app, "release_id", release.ID)
		return errors.New("formation not found")
	} else if err != nil {
		logger.Error("formation error", "app", app, "release_id", release.ID, "err", err)
		return err
	}

	// Another API process may have already published the formation. That does
	// not mean the primary is writable. Returning here lets this process run
	// DDL while semi-sync has no slave: the statement waits forever for an
	// ack, locks the privilege tables, and /status stops answering, so the
	// replicas never start.
	if alreadyScaledStillWaits(formation.Processes[procName]) {
		logger.Info("database is running, waiting until it is read-write")
		return waitForReadWrite(serviceAddr, logger)
	}

	// Copy processes and increase database processes.
	processes := make(map[string]int, len(formation.Processes))
	for k, v := range formation.Processes {
		processes[k] = v
	}

	if singleton == "true" {
		processes[procName] = 1
	} else {
		processes[procName] = 3
	}

	// Update formation.
	logger.Info("updating formation", "app", app, "release_id", release.ID)
	formation.Processes = processes
	if err := client.PutFormation(formation); err != nil {
		logger.Error("put formation error", "app", app, "release_id", release.ID, "err", err)
		return err
	}

	return waitForReadWrite(serviceAddr, logger)
}

// alreadyScaledStillWaits is true when another ScaleUp already published
// database processes. The caller must wait for read-write before DDL.
func alreadyScaledStillWaits(processCount int) bool {
	return processCount > 0
}

func waitForReadWrite(serviceAddr string, logger log15.Logger) error {
	sc := sirenia.NewClient(serviceAddr)
	if err := sc.WaitForReadWrite(5 * time.Minute); err != nil {
		logger.Error("wait for read write", "err", err)
		return errors.New("timed out while starting sirenia cluster")
	}
	logger.Info("scaling complete")
	return nil
}

// CheckScale examines sirenia cluster formation to check if cluster
// has been scaled up yet.
// Returns true if scaled, false if not.
func CheckScale(app, controllerKey, procName string, logger log15.Logger) (bool, error) {
	logger = logger.New("fn", "CheckScale")
	// Connect to controller.
	logger.Info("connecting to controller")
	client, err := controller.NewClient("", controllerKey)
	if err != nil {
		logger.Error("controller client error", "err", err)
		return false, err
	}

	// Retrieve app release.
	logger.Info("retrieving app release", "app", app)
	release, err := client.GetAppRelease(app)
	if err == controller.ErrNotFound {
		logger.Error("release not found", "app", app)
		return false, err
	} else if err != nil {
		logger.Error("get release error", "app", app, "err", err)
		return false, err
	}

	// Retrieve current formation.
	logger.Info("retrieving formation", "app", app, "release_id", release.ID)
	formation, err := client.GetFormation(app, release.ID)
	if err == controller.ErrNotFound {
		logger.Error("formation not found", "app", app, "release_id", release.ID)
		return false, err
	} else if err != nil {
		logger.Error("formation error", "app", app, "release_id", release.ID, "err", err)
		return false, err
	}

	// Database hasn't been scaled up yet
	if formation.Processes[procName] == 0 {
		return false, nil
	}

	return true, nil

}

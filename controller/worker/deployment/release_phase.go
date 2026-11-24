package deployment

import (
	"errors"
	"fmt"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func (d *DeployJob) runReleasePhase(log log15.Logger) error {
	req := ct.ReleasePhaseNewJob(d.newRelease)
	if req == nil {
		return nil
	}
	log = log.New("fn", "runReleasePhase")

	var jobs []*ct.Job
	err := d.retryAPI("list jobs before the release phase", log, func() (err error) {
		jobs, err = d.client.JobList(d.AppID)
		return err
	})
	if err != nil {
		log.Error("error listing jobs before release phase", "err", err)
		return err
	}
	if ct.ReleasePhaseCompleted(jobs, d.NewReleaseID) {
		log.Info("release command already completed")
		return nil
	}

	cmd := ct.ProcessDisplayCommand(d.newRelease.Processes[ct.ProcessTypeRelease])
	log.Info("running release command", "command", cmd)
	job, err := d.client.RunJobDetached(d.AppID, req)
	if err != nil {
		log.Error("error starting release command", "err", err)
		return fmt.Errorf("error running release command: %s", err)
	}
	if err := d.waitReleaseJob(job.ID, log); err != nil {
		return err
	}
	log.Info("release command complete")
	return nil
}

func (d *DeployJob) waitReleaseJob(jobID string, log log15.Logger) error {
	timeout := d.timeout
	if timeout <= 0 {
		timeout = time.Duration(ct.DefaultDeployTimeout) * time.Second
	}
	deadline := time.Now().Add(timeout)
	polls := newMissedPolls("waiting for the release command", "GetJob")
	for {
		if d.stop != nil {
			select {
			case <-d.stop:
				return fmt.Errorf("release command cancelled")
			default:
			}
		}
		job, err := d.client.GetJob(d.AppID, jobID)
		if err != nil {
			// A missed poll (EDGE-65). A 404 is retried here too: the
			// job's row can lag the run request. 401/403 end the wait.
			if err == controller.ErrNotFound {
				polls.miss(errors.New("job not found yet"), deadline, log)
			} else if err := polls.miss(err, deadline, log); err != nil {
				return err
			}
		} else {
			polls.answered(log)
		}
		if err == nil && job != nil && job.State == ct.JobStateDown {
			if job.ExitStatus != nil && *job.ExitStatus != 0 {
				return ct.ReleasePhaseExitError(int(*job.ExitStatus))
			}
			return nil
		}
		if time.Now().After(deadline) {
			log.Error("release command timed out", "timeout", timeout, "err", err)
			return polls.timedOut(fmt.Sprintf("release command timed out after %s", timeout))
		}
		time.Sleep(polls.wait(time.Second))
	}
}

package deployment

import (
	"fmt"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

func (d *DeployJob) runReleasePhase(log log15.Logger) error {
	req := ct.ReleasePhaseNewJob(d.newRelease)
	if req == nil {
		return nil
	}
	log = log.New("fn", "runReleasePhase")

	jobs, err := d.client.JobList(d.AppID)
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
	for {
		if d.stop != nil {
			select {
			case <-d.stop:
				return fmt.Errorf("release command cancelled")
			default:
			}
		}
		job, err := d.client.GetJob(d.AppID, jobID)
		if err == nil && job != nil && job.State == ct.JobStateDown {
			if job.ExitStatus != nil && *job.ExitStatus != 0 {
				return ct.ReleasePhaseExitError(int(*job.ExitStatus))
			}
			return nil
		}
		if time.Now().After(deadline) {
			log.Error("release command timed out", "timeout", timeout, "err", err)
			return fmt.Errorf("release command timed out after %s", timeout)
		}
		time.Sleep(time.Second)
	}
}

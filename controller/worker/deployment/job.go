package deployment

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	worker "github.com/randy-girard/flynn/controller/worker/types"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
)

type DeployJob struct {
	*ct.Deployment
	client       controller.Client
	deployEvents chan<- ct.DeploymentEvent
	logger       log15.Logger
	oldRelease   *ct.Release
	newRelease   *ct.Release
	oldFormation *ct.Formation
	newFormation *ct.Formation
	timeout      time.Duration
	stop         chan struct{}
}

func (d *DeployJob) Perform() error {
	log := d.logger.New("fn", "Perform", "deployment_id", d.ID, "app_id", d.AppID)

	log.Info("validating deployment strategy")
	var deployFunc func() error
	switch d.Strategy {
	case "one-by-one":
		deployFunc = d.deployOneByOne
	case "one-down-one-up":
		deployFunc = d.deployOneDownOneUp
	case "in-batches":
		deployFunc = d.deployInBatches
	case "all-at-once":
		deployFunc = d.deployAllAtOnce
	case "sirenia":
		deployFunc = d.deploySirenia
	case "discoverd-meta":
		deployFunc = d.deployDiscoverdMeta
	default:
		err := UnknownStrategyError{d.Strategy}
		log.Error("error validating deployment strategy", "err", err)
		return err
	}

	log.Info("getting old release", "release.id", d.OldReleaseID)
	var err error
	d.oldRelease, err = d.client.GetRelease(d.OldReleaseID)
	if err != nil {
		log.Error("error getting old release", "release.id", d.OldReleaseID, "err", err)
		return err
	}
	d.oldFormation, err = d.client.GetFormation(d.AppID, d.OldReleaseID)
	if err != nil {
		log.Error("error getting old formation", "release.id", d.OldReleaseID, "err", err)
		return err
	}

	log.Info("getting new release", "release.id", d.NewReleaseID)
	d.newRelease, err = d.client.GetRelease(d.NewReleaseID)
	if err != nil {
		log.Error("error getting new release", "release.id", d.NewReleaseID, "err", err)
		return err
	}
	d.newFormation, err = d.client.GetFormation(d.AppID, d.NewReleaseID)
	if err == controller.ErrNotFound {
		d.newFormation = &ct.Formation{
			AppID:     d.AppID,
			ReleaseID: d.NewReleaseID,
			Tags:      d.Tags,
		}
	} else if err != nil {
		return err
	}
	if d.newFormation.Processes == nil {
		d.newFormation.Processes = make(map[string]int)
	}
	d.Processes = ct.WithoutReleaseProcessCounts(d.Processes)
	if d.oldFormation != nil {
		d.oldFormation.Processes = ct.WithoutReleaseProcessCounts(d.oldFormation.Processes)
	}
	d.newFormation.Processes = ct.WithoutReleaseProcessCounts(d.newFormation.Processes)

	if processesEqual(d.newFormation.Processes, d.Processes) {
		if d.Strategy == "sirenia" && d.sireniaOldReleaseActive() {
			log.Info("sirenia new formation matches target but old release still active, continuing deploy")
		} else if d.oldReleaseStillActive() {
			// one-by-one omni (controller scheduler) starts the new
			// formation first. A scale timeout can leave both releases
			// running; the next deploy of the same release must finish
			// draining the old jobs instead of no-op'ing.
			log.Info("new formation matches target but old release still active, continuing deploy")
		} else {
			log.Info("deployment already completed, nothing to do")
			return nil
		}
	}

	d.timeout = time.Duration(d.DeployTimeout) * time.Second

	log.Info(
		"determined deployment state",
		"original", d.Processes,
		"old_release", d.oldFormation.Processes,
		"new_release", d.newFormation.Processes,
	)
	if err := d.runReleasePhase(log); err != nil {
		return err
	}
	return deployFunc()
}

func (d *DeployJob) scaleOldRelease(wait bool) error {
	opts := ct.ScaleOptions{
		Processes:        d.oldFormation.Processes,
		Tags:             d.oldFormation.Tags,
		Timeout:          &d.timeout,
		Stop:             d.stop,
		NoWait:           !wait,
		JobEventCallback: d.logJobEvent,
	}
	err := d.client.ScaleAppRelease(d.AppID, d.OldReleaseID, opts)
	if err == ct.ErrScalingStopped {
		err = worker.ErrStopped
	}
	return err
}

// failedJobThreshold is the number of times new jobs can fail when scaling up
// a new release before aborting the deploy
const newJobFailureThreshold = 5

func (d *DeployJob) scaleNewRelease() error {
	return d.scaleNewReleaseWait(true)
}

func (d *DeployJob) scaleNewReleaseWait(wait bool) error {
	failures := 0
	opts := ct.ScaleOptions{
		Processes: d.newFormation.Processes,
		Tags:      d.newFormation.Tags,
		Timeout:   &d.timeout,
		Stop:      d.stop,
		NoWait:    !wait,
		JobEventCallback: func(job *ct.Job) error {
			d.logJobEvent(job)
			if !wait {
				return nil
			}
			if job.State == ct.JobStateDown {
				failures++
				if failures <= newJobFailureThreshold {
					d.logger.Warn("ignoring down job event for new release", "count", failures, "err", job.HostError)
					return nil
				}
				return d.jobStartFailure(job)
			}
			return nil
		},
	}
	err := d.client.ScaleAppRelease(d.AppID, d.NewReleaseID, opts)
	if err == ct.ErrScalingStopped {
		err = worker.ErrStopped
	}
	return err
}

func (d *DeployJob) jobStartFailure(job *ct.Job) error {
	hint := ""
	if job == nil || job.HostError == nil || strings.TrimSpace(*job.HostError) == "" {
		hint = d.jobCrashHint(job)
	}
	return jobStartFailure(job, hint)
}

func jobStartFailure(job *ct.Job, extras ...string) error {
	typ := "app"
	if job != nil && job.Type != "" {
		typ = job.Type
	}
	var parts []string
	if job != nil {
		if job.HostError != nil && strings.TrimSpace(*job.HostError) != "" {
			parts = append(parts, strings.TrimSpace(*job.HostError))
		}
		if job.ExitStatus != nil {
			parts = append(parts, fmt.Sprintf("exit %d", *job.ExitStatus))
		}
		if job.Restarts != nil && *job.Restarts > 0 {
			parts = append(parts, fmt.Sprintf("restarts %d", *job.Restarts))
		}
		if name := strings.TrimSpace(job.Name); name != "" {
			parts = append(parts, name)
		} else if id := strings.TrimSpace(job.ID); id != "" {
			parts = append(parts, id)
		}
	}
	for _, extra := range extras {
		if s := strings.TrimSpace(extra); s != "" {
			parts = append(parts, s)
		}
	}
	msg := "got down job event"
	if len(parts) > 0 {
		msg = strings.Join(parts, "; ")
	}
	return fmt.Errorf("%s job failed to start: %s", typ, msg)
}

const jobCrashHintTimeout = 2 * time.Second

func (d *DeployJob) jobCrashHint(job *ct.Job) string {
	if d == nil || d.client == nil || job == nil {
		return ""
	}
	jobID := strings.TrimSpace(job.ID)
	if jobID == "" {
		jobID = strings.TrimSpace(job.UUID)
	}
	if jobID == "" {
		return ""
	}
	appID := d.AppID
	if appID == "" && job.AppID != "" {
		appID = job.AppID
	}
	if appID == "" {
		return ""
	}
	done := make(chan string, 1)
	go func() {
		done <- fetchJobCrashHint(d.client, appID, jobID)
	}()
	select {
	case hint := <-done:
		return hint
	case <-time.After(jobCrashHintTimeout):
		return ""
	}
}

func fetchJobCrashHint(client controller.Client, appID, jobID string) string {
	if client == nil {
		return ""
	}
	lines := 50
	rc, err := client.GetAppLog(appID, &logagg.LogOpts{
		JobID: jobID,
		Lines: &lines,
		StreamTypes: []logagg.StreamType{
			logagg.StreamTypeStdout,
			logagg.StreamTypeStderr,
			logagg.StreamTypeSystem,
			logagg.StreamTypeInit,
		},
	})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var msgs []string
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Msg string `json:"msg"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return crashHintFromLogLines(msgs)
		}
		if strings.TrimSpace(msg.Msg) != "" {
			msgs = append(msgs, msg.Msg)
		}
	}
	return crashHintFromLogLines(msgs)
}

func crashHintFromLogLines(msgs []string) string {
	var load, typed, crashed string
	for _, m := range msgs {
		s := strings.TrimSpace(m)
		if s == "" || strings.HasPrefix(s, "from ") {
			continue
		}
		if strings.Contains(s, "cpu_percent=") {
			continue
		}
		low := strings.ToLower(s)
		switch {
		case strings.Contains(low, "unable to load"):
			load = strings.TrimPrefix(s, "! ")
		case strings.Contains(s, "(LoadError)") || strings.Contains(s, "(RuntimeError)") || strings.Contains(s, "(NameError)") || strings.Contains(low, "fatal:") || strings.Contains(low, "panic:"):
			typed = s
		case strings.Contains(low, "process crashed") || strings.Contains(low, "job exited"):
			crashed = s
		}
	}
	hint := load
	if hint == "" {
		hint = typed
	}
	if hint == "" {
		hint = crashed
	}
	if hint == "" {
		return ""
	}
	if len(hint) > 240 {
		return hint[:240]
	}
	return hint
}

func (d *DeployJob) logJobEvent(job *ct.Job) error {
	d.logger.Info(
		"got job event",
		"release.id", job.ReleaseID,
		"job.id", job.ID,
		"job.type", job.Type,
		"job.state", job.State,
	)
	return nil
}

func (d *DeployJob) scaleOneByOne(typ string, log log15.Logger) error {
	if d.processIsOmni(typ) {
		return d.scaleOmniOneDownOneUp(typ, log)
	}
	return d.scaleUpDownInBatches(typ, 1, log)
}

func (d *DeployJob) scaleUpDownInBatches(typ string, batchCount int, log log15.Logger) error {
	for i := 0; i < d.Processes[typ]; i += batchCount {
		if err := d.scaleNewFormationUp(typ, batchCount, log); err != nil {
			return err
		}

		if err := d.scaleOldFormationDown(typ, batchCount, log); err != nil {
			return err
		}
	}
	return nil
}

func (d *DeployJob) scaleOneDownOneUp(typ string, log log15.Logger) error {
	if d.processIsOmni(typ) {
		return d.scaleOmniOneDownOneUp(typ, log)
	}
	for i := 0; i < d.Processes[typ]; i++ {
		if err := d.scaleOldFormationDownByOne(typ, log); err != nil {
			return err
		}
		if err := d.scaleNewFormationUpByOne(typ, log); err != nil {
			return err
		}
	}
	return nil
}

func (d *DeployJob) scaleNewFormationUpByOne(typ string, log log15.Logger) error {
	return d.scaleNewFormationUp(typ, 1, log)
}

func (d *DeployJob) scaleNewFormationUp(typ string, count int, log log15.Logger) error {
	// only scale new processes which still exist
	if _, ok := d.newRelease.Processes[typ]; !ok {
		return nil
	}
	// don't scale higher than d.Processes
	if d.newFormation.Processes[typ] == d.Processes[typ] {
		return nil
	}
	log.Info("scaling new formation up", "release.id", d.NewReleaseID, "job.type", typ, "count", count)
	d.newFormation.Processes[typ] += count
	// don't scale higher than d.Processes
	if d.newFormation.Processes[typ] > d.Processes[typ] {
		d.newFormation.Processes[typ] = d.Processes[typ]
	}
	if err := d.scaleNewRelease(); err != nil {
		log.Error("error scaling new formation up", "release.id", d.NewReleaseID, "job.type", typ, "count", count, "err", err)
		return err
	}
	return nil
}

func (d *DeployJob) scaleOldFormationDownByOne(typ string, log log15.Logger) error {
	return d.scaleOldFormationDown(typ, 1, log)
}

func (d *DeployJob) scaleOldFormationDown(typ string, count int, log log15.Logger) error {
	// don't scale lower than zero
	if d.oldFormation.Processes[typ] == 0 {
		return nil
	}
	log.Info("scaling old formation down", "release.id", d.OldReleaseID, "job.type", typ, "count", count)
	d.oldFormation.Processes[typ] -= count
	// don't scale lower than zero
	if d.oldFormation.Processes[typ] < 0 {
		d.oldFormation.Processes[typ] = 0
	}
	if err := d.scaleOldRelease(true); err != nil {
		log.Error("error scaling old formation down", "release.id", d.OldReleaseID, "job.type", typ, "count", count, "err", err)
		return err
	}
	return nil
}

func processesEqual(a map[string]int, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for typ, countA := range a {
		if countB, ok := b[typ]; !ok || countA != countB {
			return false
		}
	}
	return true
}

func (d *DeployJob) sireniaProcessType() string {
	if d.newRelease != nil && d.newRelease.Env["SIRENIA_PROCESS"] != "" {
		return d.newRelease.Env["SIRENIA_PROCESS"]
	}
	if d.oldRelease != nil && d.oldRelease.Env["SIRENIA_PROCESS"] != "" {
		return d.oldRelease.Env["SIRENIA_PROCESS"]
	}
	return ""
}

func (d *DeployJob) sireniaOldReleaseActive() bool {
	if d.oldFormation == nil {
		return false
	}
	processType := d.sireniaProcessType()
	if processType == "" {
		return false
	}
	return d.oldFormation.Processes[processType] > 0
}

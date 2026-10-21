package main

import (
	"testing"

	"github.com/inconshreveable/log15"
	host "github.com/randy-girard/flynn/host/types"
)

func TestSendMemoryEventUsesRunningJobIdentity(t *testing.T) {
	const jobID = "devnode1-job"
	d := NewWebhookDispatcher("devnode1", &State{
		jobs: map[string]*host.ActiveJob{
			jobID: {
				Job: &host.Job{
					ID: jobID,
					Metadata: map[string]string{
						host.MetaControllerApp:     "app-estuary",
						host.MetaControllerAppName: "kafka-estuary-73491",
						host.MetaControllerType:    "kafka",
					},
					Config: host.ContainerConfig{Env: map[string]string{
						"FLYNN_APP_ID":       "app-estuary",
						"FLYNN_APP_NAME":     "kafka-estuary-73491",
						"FLYNN_PROCESS_TYPE": "kafka",
						"FLYNN_RELEASE_ID":   "rel-1",
					}},
				},
			},
		},
	}, log15.New())

	d.Send(host.CodeMemorySoft, "Soft memory limit exceeded", host.SeverityWarning, jobID, nil, map[string]string{
		"usage_bytes": "1",
	})

	select {
	case ev := <-d.events:
		if ev.AppID != "app-estuary" || ev.AppName != "kafka-estuary-73491" || ev.ProcessType != "kafka" || ev.ReleaseID != "rel-1" {
			t.Fatalf("identity %+v", ev)
		}
		if ev.JobID != jobID || ev.HostID != "devnode1" || ev.Code != host.CodeMemorySoft {
			t.Fatalf("event %+v", ev)
		}
		if ev.Job == nil || ev.Job.ID != jobID {
			t.Fatalf("job %+v", ev.Job)
		}
	default:
		t.Fatal("event was not enqueued")
	}
}

func TestSendMemoryEventWithoutJobStaysHostOnly(t *testing.T) {
	d := NewWebhookDispatcher("devnode1", nil, log15.New())
	d.Send(host.CodeMemoryHard, "Hard memory limit exceeded (OOM kill)", host.SeverityCritical, "missing-job", nil, nil)
	ev := <-d.events
	if ev.AppID != "" || ev.AppName != "" || ev.ProcessType != "" || ev.JobID != "missing-job" {
		t.Fatalf("event %+v", ev)
	}
}

func TestApplyWebhookJobIdentityPrefersEnvAndFillsFromMetadata(t *testing.T) {
	ev := &host.WebhookEvent{}
	applyWebhookJobIdentity(ev, &host.ActiveJob{Job: &host.Job{
		ID: "job-1",
		Metadata: map[string]string{
			host.MetaControllerAppName: "from-meta",
			host.MetaControllerType:    "kafka",
			"flynn-controller.release": "rel-meta",
		},
	}})
	if ev.AppID != "" || ev.AppName != "from-meta" || ev.ProcessType != "kafka" || ev.ReleaseID != "rel-meta" || ev.JobID != "job-1" {
		t.Fatalf("%+v", ev)
	}
}

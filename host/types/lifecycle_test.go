package host

import "testing"

func sampleJob(reason, profile, procType string) *ActiveJob {
	meta := map[string]string{
		MetaControllerType: procType,
	}
	if reason != "" {
		meta[MetaControllerReason] = reason
	}
	if profile != "" {
		meta[MetaControllerRuntimeProfile] = profile
	}
	return &ActiveJob{
		Job: &Job{
			ID:       "host-abc",
			Metadata: meta,
			Config:   ContainerConfig{Env: map[string]string{"FLYNN_PROCESS_TYPE": procType}},
		},
	}
}

func TestFormatJobLifecycleLog(t *testing.T) {
	cases := []struct {
		event JobEventType
		job   *ActiveJob
		want  string
	}{
		{JobEventCreate, sampleJob("", "large", "web"), "Starting web process (runtime profile large)"},
		{JobEventCreate, sampleJob(JobReasonRestart, "", "web"), "Restarting web process"},
		{JobEventCreate, sampleJob(JobReasonReplace, "large", "web"), "Replacing web process (runtime profile large)"},
		{JobEventCreate, sampleJob(JobReasonScale, "", "web"), "Scaling up web process"},
		{JobEventStart, sampleJob("", "medium", "web"), "web process started (runtime profile medium)"},
		{JobEventStart, namedSampleJob("web.4821", "medium", "web"), "web process started (web.4821, runtime profile medium)"},
		{JobEventStop, sampleJob("", "", "web"), "web process stopped"},
		{JobEventStop, sampleStopJob(JobReasonScaleDown, "web"), "Scaling down web process"},
		{JobEventError, &ActiveJob{Job: &Job{Metadata: map[string]string{MetaControllerType: "web"}}, Error: strPtr("boom")}, "web process failed to start: boom"},
	}
	exit := 1
	crashed := sampleJob("", "", "web")
	crashed.Status = StatusCrashed
	crashed.ExitStatus = &exit
	cases = append(cases, struct {
		event JobEventType
		job   *ActiveJob
		want  string
	}{JobEventStop, crashed, "web process crashed (exit 1)"})

	for _, tc := range cases {
		got := FormatJobLifecycleLog(tc.event, tc.job)
		if got != tc.want {
			t.Errorf("event=%s got %q want %q", tc.event, got, tc.want)
		}
	}
	if FormatJobLifecycleLog(JobEventCleanup, sampleJob("", "", "web")) != "" {
		t.Fatal("cleanup must not write an app log line")
	}
}

func TestJobLifecycleWebhookCodes(t *testing.T) {
	code, desc, sev := JobLifecycleWebhook(JobEventCreate, sampleJob(JobReasonRestart, "", "web"))
	if code != CodeJobRestart || sev != SeverityInfo || desc != "Restarting web process" {
		t.Fatalf("restart: %s %s %s", code, sev, desc)
	}
	code, _, _ = JobLifecycleWebhook(JobEventCreate, sampleJob(JobReasonReplace, "", "web"))
	if code != CodeJobReplace {
		t.Fatalf("replace code=%s", code)
	}
	code, _, _ = JobLifecycleWebhook(JobEventCreate, sampleJob(JobReasonScale, "", "web"))
	if code != CodeJobScaleUp {
		t.Fatalf("scale code=%s", code)
	}
	scaledown := sampleStopJob(JobReasonScaleDown, "web")
	scaledown.Status = StatusCrashed
	exit := 143
	scaledown.ExitStatus = &exit
	code, desc, sev = JobLifecycleWebhook(JobEventStop, scaledown)
	if code != CodeJobScaleDown || sev != SeverityInfo || desc != "Scaling down web process" {
		t.Fatalf("scale-down: %s %s %s", code, sev, desc)
	}
	code, _, sev = JobLifecycleWebhook(JobEventStop, sampleJob("", "", "web"))
	if code != CodeJobStop || sev != SeverityInfo {
		t.Fatalf("stop: %s %s", code, sev)
	}
	crashed := sampleJob("", "", "web")
	crashed.Status = StatusCrashed
	code, _, sev = JobLifecycleWebhook(JobEventStop, crashed)
	if code != CodeJobCrash || sev != SeverityError {
		t.Fatalf("crash: %s %s", code, sev)
	}
}

func namedSampleJob(name, profile, procType string) *ActiveJob {
	job := sampleJob("", profile, procType)
	job.Job.Metadata[MetaControllerName] = name
	return job
}

func sampleStopJob(reason, procType string) *ActiveJob {
	job := sampleJob("", "", procType)
	job.Job.Metadata[MetaControllerStopReason] = reason
	return job
}

func strPtr(s string) *string { return &s }

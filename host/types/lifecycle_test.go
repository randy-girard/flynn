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
		meta[MetaControllerRuntime] = profile
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
		{JobEventCreate, sampleJob("", "large", "web"), "Starting web process (runtime large)"},
		{JobEventCreate, sampleJob(JobReasonRestart, "", "web"), "Restarting web process"},
		{JobEventCreate, sampleJob(JobReasonReplace, "large", "web"), "Scaling up web process (runtime large)"},
		{JobEventCreate, sampleJob(JobReasonScale, "", "web"), "Scaling up web process"},
		{JobEventStart, sampleJob("", "medium", "web"), "web process started (runtime medium)"},
		{JobEventStart, namedSampleJob("web.4821", "medium", "web"), "web process started (web.4821, runtime medium)"},
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

func TestFormatJobLifecycleLogIncludesCommand(t *testing.T) {
	job := sampleJob(JobReasonScale, "", "web")
	job.Job.Config.Args = []string{"/runner/init", "start", "web"}
	got := FormatJobLifecycleLog(JobEventCreate, job)
	want := "Scaling up web process with command `/runner/init start web`"
	if got != want {
		t.Fatalf("create: %q want %q", got, want)
	}
	got = FormatJobLifecycleLog(JobEventStart, job)
	want = "web process started with command `/runner/init start web`"
	if got != want {
		t.Fatalf("start: %q want %q", got, want)
	}
	if JobCommand(nil) != "" || JobCommand(&ActiveJob{}) != "" {
		t.Fatal("empty job must have no command")
	}
}

func TestFormatJobLifecycleLogIncludesProcfileCommand(t *testing.T) {
	job := sampleJob(JobReasonScale, "small", "web")
	job.Job.Metadata[MetaControllerName] = "web.4530"
	job.Job.Config.Args = []string{"/runner/init", "start", "web"}
	StampJobCommand(job.Job, "bundle exec puma -C config/puma.rb")
	got := FormatJobLifecycleLog(JobEventCreate, job)
	want := "Scaling up web process with command `bundle exec puma -C config/puma.rb` (`/runner/init start web`) (web.4530, runtime small)"
	if got != want {
		t.Fatalf("create: %q want %q", got, want)
	}
	got = FormatJobLifecycleLog(JobEventStart, job)
	want = "web process started with command `bundle exec puma -C config/puma.rb` (`/runner/init start web`) (web.4530, runtime small)"
	if got != want {
		t.Fatalf("start: %q want %q", got, want)
	}
	docker := sampleJob(JobReasonScale, "", "web")
	docker.Job.Config.Args = []string{"/bin/app", "serve"}
	StampJobCommand(docker.Job, "/bin/app serve")
	got = FormatJobLifecycleLog(JobEventStart, docker)
	if got != "web process started with command `/bin/app serve`" {
		t.Fatalf("docker command must not be duplicated: %q", got)
	}
}

func TestJobShortName(t *testing.T) {
	if got := JobShortName(nil); got != "" {
		t.Fatalf("nil=%q", got)
	}
	job := &Job{Metadata: map[string]string{MetaControllerName: "web.4821"}}
	if got := JobShortName(job); got != "web.4821" {
		t.Fatalf("meta=%q", got)
	}
	job = &Job{Config: ContainerConfig{Env: map[string]string{"FLYNN_JOB_NAME": "typ.9"}}}
	if got := JobShortName(job); got != "typ.9" {
		t.Fatalf("env=%q", got)
	}
}

func TestJobDisplayNameFallsBackToTypeAndUUID(t *testing.T) {
	job := &Job{
		ID:       "devnode1-97100ccb-1b45-4af1-9c71-78e1d3571042",
		Metadata: map[string]string{MetaControllerType: "scheduler"},
	}
	got := JobDisplayName(job)
	if got != "scheduler.8676" {
		t.Fatalf("display=%q", got)
	}
	if JobShortName(job) != "" {
		t.Fatal("unallocated jobs have no short name until EnsureJobProcessName")
	}
	if EnsureJobProcessName(job) != "scheduler.8676" {
		t.Fatalf("ensure=%q", JobShortName(job))
	}
	if job.Metadata[MetaControllerName] != "scheduler.8676" || job.Config.Env["FLYNN_JOB_NAME"] != "scheduler.8676" {
		t.Fatalf("stamped meta=%v env=%v", job.Metadata, job.Config.Env)
	}
	job.Metadata[MetaControllerName] = "web.5600"
	if JobDisplayName(job) != "web.5600" {
		t.Fatalf("allocated name must win: %q", JobDisplayName(job))
	}
}

func TestJobLifecycleWebhookCodes(t *testing.T) {
	code, desc, sev := JobLifecycleWebhook(JobEventCreate, sampleJob(JobReasonRestart, "", "web"))
	if code != CodeJobRestart || sev != SeverityInfo || desc != "Restarting web process" {
		t.Fatalf("restart: %s %s %s", code, sev, desc)
	}
	code, _, _ = JobLifecycleWebhook(JobEventCreate, sampleJob(JobReasonReplace, "", "web"))
	if code != CodeJobScaleUp {
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

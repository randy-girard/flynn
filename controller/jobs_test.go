package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	tu "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/random"
	"golang.org/x/net/context"
)

func (s *S) createTestJob(c *C, in *ct.Job) *ct.Job {
	c.Assert(s.c.PutJob(in), IsNil)
	return in
}

func (s *S) TestJobList(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "job-list"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	s.createTestFormation(c, &ct.Formation{ReleaseID: release.ID, AppID: app.ID})
	id := random.UUID()
	s.createTestJob(c, &ct.Job{UUID: id, AppID: app.ID, ReleaseID: release.ID, Type: "web", State: ct.JobStateStarting, Meta: map[string]string{"some": "info"}})

	list, err := s.c.JobList(app.ID)
	c.Assert(err, IsNil)
	c.Assert(len(list), Equals, 1)
	job := list[0]
	c.Assert(job.UUID, Equals, id)
	c.Assert(job.AppID, Equals, app.ID)
	c.Assert(job.ReleaseID, Equals, release.ID)
	c.Assert(job.Meta, DeepEquals, map[string]string{"some": "info"})
}

func (s *S) TestJobListActive(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "job-list-active"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})

	// mark all existing jobs as down
	c.Assert(s.hc.db.Exec("UPDATE job_cache SET state = 'down'"), IsNil)

	createJob := func(state ct.JobState) *ct.Job {
		return s.createTestJob(c, &ct.Job{
			UUID:      random.UUID(),
			AppID:     app.ID,
			ReleaseID: release.ID,
			Type:      "web",
			State:     state,
			Meta:      map[string]string{"some": "info"},
		})
	}

	jobs := []*ct.Job{
		createJob(ct.JobStatePending),
		createJob(ct.JobStateStarting),
		createJob(ct.JobStateUp),
		createJob(ct.JobStateStopping),
		createJob(ct.JobStateDown),
		createJob(ct.JobStatePending),
		createJob(ct.JobStateStarting),
		createJob(ct.JobStateUp),
	}

	list, err := s.c.JobListActive()
	c.Assert(err, IsNil)
	c.Assert(list, HasLen, 7)

	// check that we only get jobs with a pending, starting, up or stopping
	// state, most recently updated first
	expected := []*ct.Job{jobs[7], jobs[6], jobs[5], jobs[3], jobs[2], jobs[1], jobs[0]}
	for i, job := range expected {
		actual := list[i]
		c.Assert(actual.UUID, Equals, job.UUID)
		c.Assert(actual.State, Equals, job.State)
	}
}

func (s *S) TestJobGet(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "job-get"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	s.createTestFormation(c, &ct.Formation{ReleaseID: release.ID, AppID: app.ID})
	uuid := random.UUID()
	hostID := "host0"
	jobID := cluster.GenerateJobID(hostID, uuid)
	s.createTestJob(c, &ct.Job{
		ID:        jobID,
		UUID:      uuid,
		HostID:    hostID,
		AppID:     app.ID,
		ReleaseID: release.ID,
		Type:      "web",
		Name:      "web.4821",
		State:     ct.JobStateStarting,
		Meta:      map[string]string{"some": "info"},
	})

	// test getting the job with the cluster ID, UUID, and short name
	for _, id := range []string{jobID, uuid, "web.4821"} {
		job, err := s.c.GetJob(app.ID, id)
		c.Assert(err, IsNil)
		c.Assert(job.ID, Equals, jobID)
		c.Assert(job.UUID, Equals, uuid)
		c.Assert(job.HostID, Equals, hostID)
		c.Assert(job.AppID, Equals, app.ID)
		c.Assert(job.ReleaseID, Equals, release.ID)
		c.Assert(job.Name, Equals, "web.4821")
		c.Assert(job.Meta["some"], Equals, "info")
		c.Assert(job.Meta["name"], Equals, "web.4821")
	}
}

func (s *S) TestJobStateChanges(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "job-state-changes"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	s.createTestFormation(c, &ct.Formation{ReleaseID: release.ID, AppID: app.ID})

	// add a new pending job
	uuid := random.UUID()
	job := &ct.Job{
		ID:        cluster.GenerateJobID("host0", uuid),
		UUID:      uuid,
		AppID:     app.ID,
		ReleaseID: release.ID,
		Type:      "web",
		State:     ct.JobStatePending,
	}
	c.Assert(s.c.PutJob(job), IsNil)
	gotJob, err := s.c.GetJob(app.ID, job.ID)
	c.Assert(err, IsNil)
	c.Assert(gotJob.State, Equals, ct.JobStatePending)

	// update the state to starting
	job.State = ct.JobStateStarting
	c.Assert(s.c.PutJob(job), IsNil)
	gotJob, err = s.c.GetJob(app.ID, job.ID)
	c.Assert(err, IsNil)
	c.Assert(gotJob.State, Equals, ct.JobStateStarting)

	// update the state to up
	job.State = ct.JobStateUp
	c.Assert(s.c.PutJob(job), IsNil)
	gotJob, err = s.c.GetJob(app.ID, job.ID)
	c.Assert(err, IsNil)
	c.Assert(gotJob.State, Equals, ct.JobStateUp)

	// delete the app and check we can still mark the job as down
	c.Assert(s.hc.db.Exec("app_delete", app.ID), IsNil)
	job.State = ct.JobStateDown
	c.Assert(s.c.PutJob(job), IsNil)
	gotJob, err = s.c.GetJob(app.ID, job.ID)
	c.Assert(err, IsNil)
	c.Assert(gotJob.State, Equals, ct.JobStateDown)
}

func fakeHostID() string {
	return random.Hex(16)
}

func (s *S) TestKillJob(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "killjob"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	hostID := fakeHostID()
	uuid := random.UUID()
	jobID := cluster.GenerateJobID(hostID, uuid)
	s.createTestJob(c, &ct.Job{
		ID:        jobID,
		UUID:      uuid,
		HostID:    hostID,
		AppID:     app.ID,
		ReleaseID: release.ID,
		Type:      "web",
		State:     ct.JobStateStarting,
		Meta:      map[string]string{"some": "info"},
	})
	hc := tu.NewFakeHostClient(hostID, false)
	hc.AddJob(&host.Job{ID: jobID})
	s.cc.AddHost(hc)

	err := s.c.DeleteJob(app.ID, jobID)
	c.Assert(err, IsNil)
	c.Assert(hc.IsStopped(jobID), Equals, true)
}

func (s *S) TestKillJobEmptyBody(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "killjob-empty-body"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	hostID := fakeHostID()
	uuid := random.UUID()
	jobID := cluster.GenerateJobID(hostID, uuid)
	s.createTestJob(c, &ct.Job{
		ID:        jobID,
		UUID:      uuid,
		HostID:    hostID,
		AppID:     app.ID,
		ReleaseID: release.ID,
		Type:      "web",
		State:     ct.JobStateStarting,
	})
	hc := tu.NewFakeHostClient(hostID, false)
	hc.AddJob(&host.Job{ID: jobID})
	s.cc.AddHost(hc)

	s.deleteExpectEmpty200(c, "/apps/"+app.ID+"/jobs/"+jobID)
	c.Assert(hc.IsStopped(jobID), Equals, true)
}

func (s *S) TestRunJobDetached(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-detached"})
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hostID := fakeHostID()
	hc := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(hc)

	release := s.createTestRelease(c, app.ID, &ct.Release{
		ArtifactIDs: []string{artifact.ID},
		Env:         map[string]string{"RELEASE": "true", "FOO": "bar"},
	})

	args := []string{"foo", "bar"}
	req := &ct.NewJob{
		ReleaseID:  release.ID,
		ReleaseEnv: true,
		Args:       args,
		Env:        map[string]string{"JOB": "true", "FOO": "baz"},
		Meta:       map[string]string{"foo": "baz"},
	}
	res, err := s.c.RunJobDetached(app.ID, req)
	c.Assert(err, IsNil)
	c.Assert(res.ID, Not(Equals), "")
	c.Assert(res.ReleaseID, Equals, release.ID)
	c.Assert(res.Type, Equals, "runner")
	c.Assert(res.Args, DeepEquals, args)

	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	for _, j := range jobs {
		job := j.Job
		c.Assert(res.ID, Equals, job.ID)
		name := job.Metadata[host.MetaControllerName]
		c.Assert(ct.IsJobName(name), Equals, true)
		delete(job.Metadata, host.MetaControllerName)
		c.Assert(job.Metadata, DeepEquals, map[string]string{
			"flynn-controller.app":          app.ID,
			"flynn-controller.app_name":     app.Name,
			"flynn-controller.release":      release.ID,
			"flynn-controller.type":         "runner",
			"foo":                           "baz",
			"gc.max_inactive_slug_releases": "10",
		})
		c.Assert(job.Config.Args, DeepEquals, []string{"foo", "bar"})
		c.Assert(job.Config.Env["FLYNN_JOB_NAME"], Equals, name)
		delete(job.Config.Env, "FLYNN_JOB_NAME")
		c.Assert(job.Config.Env, DeepEquals, map[string]string{
			"FLYNN_APP_ID":       app.ID,
			"FLYNN_RELEASE_ID":   release.ID,
			"FLYNN_PROCESS_TYPE": "runner",
			"FLYNN_JOB_ID":       job.ID,
			"FOO":                "baz",
			"JOB":                "true",
			"RELEASE":            "true",
		})
		c.Assert(job.Config.Stdin, Equals, false)
	}
}

func (s *S) TestRunJobSystemAppPartition(c *C) {
	app := s.createTestApp(c, &ct.App{
		Name: "blobstore",
		Meta: map[string]string{"flynn-system-app": "true"},
	})
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hostID := fakeHostID()
	host := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(host)

	release := s.createTestRelease(c, app.ID, &ct.Release{
		ArtifactIDs: []string{artifact.ID},
	})
	res, err := s.c.RunJobDetached(app.ID, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"wget", "-qO-", "http://blobstore.discoverd/.well-known/status"},
	})
	c.Assert(err, IsNil)
	jobs, err := host.ListJobs()
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	for _, j := range jobs {
		job := j.Job
		c.Assert(job.ID, Equals, res.ID)
		c.Assert(job.Partition, Equals, "system")
		c.Assert(job.Metadata["flynn-system-app"], Equals, "true")
	}
}

func (s *S) TestRunJobAttached(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-attached"})
	hostID := fakeHostID()
	hc := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(hc)

	input := make(chan string, 1)
	var jobID string
	hc.SetAttachFunc("*", func(req *host.AttachReq, wait bool) (cluster.AttachClient, error) {
		c.Assert(wait, Equals, true)
		c.Assert(req.JobID, Not(Equals), "")
		c.Assert(req, DeepEquals, &host.AttachReq{
			JobID:  req.JobID,
			Flags:  host.AttachFlagStdout | host.AttachFlagStderr | host.AttachFlagStdin | host.AttachFlagStream,
			Height: 20,
			Width:  10,
		})
		jobID = req.JobID
		inPipeR, inPipeW := io.Pipe()
		go func() {
			// jobs.go copies via Conn() as raw bytes; read the full stdin
			// payload (a single Read can return short under -race).
			buf := make([]byte, len("test in"))
			_, err := io.ReadFull(inPipeR, buf)
			if err != nil {
				input <- err.Error()
				return
			}
			input <- string(buf)
		}()
		outPipeR, outPipeW := io.Pipe()
		go func() {
			// Do not Close the writer here: jobs.go treats EOF on either
			// attach direction as "done" and tears down the session.
			outPipeW.Write([]byte("test out"))
		}()
		return &rawAttachClient{rwc: struct {
			io.Reader
			io.WriteCloser
		}{outPipeR, inPipeW}}, nil
	})

	artifact := s.createTestArtifact(c, &ct.Artifact{})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		ArtifactIDs: []string{artifact.ID},
		Env:         map[string]string{"RELEASE": "true", "FOO": "bar"},
	})

	data := &ct.NewJob{
		ReleaseID:  release.ID,
		ReleaseEnv: true,
		Args:       []string{"foo", "bar"},
		Env:        map[string]string{"JOB": "true", "FOO": "baz"},
		Meta:       map[string]string{"foo": "baz"},
		TTY:        true,
		Columns:    10,
		Lines:      20,
	}
	rwc, err := s.c.RunJobAttached(app.ID, data)
	c.Assert(err, IsNil)
	defer rwc.Close()

	// Read stdout while writing stdin. jobs.go copies both directions on the
	// hijacked conn; waiting for stdin before reading can stall the session
	// under -race/cover (CI hung here for the full 10m package timeout).
	stdout := make(chan string, 1)
	go func() {
		buf := make([]byte, 10)
		n, err := rwc.Read(buf)
		if err != nil {
			stdout <- err.Error()
			return
		}
		stdout <- string(buf[:n])
	}()
	_, err = rwc.Write([]byte("test in"))
	c.Assert(err, IsNil)
	select {
	case got := <-input:
		c.Assert(got, Equals, "test in")
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for attach stdin")
	}
	select {
	case got := <-stdout:
		c.Assert(got, Equals, "test out")
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for attach stdout")
	}

	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	for _, j := range jobs {
		job := j.Job
		c.Assert(job.ID, Equals, jobID)
		name := job.Metadata[host.MetaControllerName]
		c.Assert(ct.IsJobName(name), Equals, true)
		delete(job.Metadata, host.MetaControllerName)
		c.Assert(job.Metadata, DeepEquals, map[string]string{
			"flynn-controller.app":          app.ID,
			"flynn-controller.app_name":     app.Name,
			"flynn-controller.release":      release.ID,
			"flynn-controller.type":         "console",
			"foo":                           "baz",
			"gc.max_inactive_slug_releases": "10",
		})
		c.Assert(job.Config.Args, DeepEquals, []string{"foo", "bar"})
		c.Assert(job.Config.Env["FLYNN_JOB_NAME"], Equals, name)
		delete(job.Config.Env, "FLYNN_JOB_NAME")
		c.Assert(job.Config.Env, DeepEquals, map[string]string{
			"FLYNN_APP_ID":       app.ID,
			"FLYNN_RELEASE_ID":   release.ID,
			"FLYNN_PROCESS_TYPE": "console",
			"FLYNN_JOB_ID":       job.ID,
			"FOO":                "baz",
			"JOB":                "true",
			"RELEASE":            "true",
		})
		c.Assert(job.Config.Stdin, Equals, true)
	}
}

// rawAttachClient is a minimal AttachClient for tests that use jobs.go's raw
// Conn() copy path (not framed AttachClient.Write).
type rawAttachClient struct {
	rwc io.ReadWriteCloser
}

func (a *rawAttachClient) Conn() io.ReadWriteCloser { return a.rwc }
func (a *rawAttachClient) Wait() error              { return nil }
func (a *rawAttachClient) Receive(io.Writer, io.Writer) (int, error) {
	return -1, nil
}
func (a *rawAttachClient) Signal(int) error               { return nil }
func (a *rawAttachClient) ResizeTTY(uint16, uint16) error { return nil }
func (a *rawAttachClient) CloseWrite() error              { return nil }
func (a *rawAttachClient) Write(p []byte) (int, error)    { return a.rwc.Write(p) }
func (a *rawAttachClient) Close() error                   { return a.rwc.Close() }

func (s *S) runJobWithToken(c *C, app *ct.App, tok *authorizer.Token, newJob *ct.NewJob) *httptest.ResponseRecorder {
	body, err := json.Marshal(newJob)
	c.Assert(err, IsNil)
	req, err := http.NewRequest("POST", "/apps/"+app.ID+"/jobs", bytes.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), "app", app)
	ctx = context.WithValue(ctx, authz.TokenContextKey, tok)
	s.api.RunJob(ctx, rec, req)
	return rec
}

func (s *S) TestRunJobAppScopedCannotSetSystemTrust(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-untrusted"})
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hostID := fakeHostID()
	hc := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(hc)
	release := s.createTestRelease(c, app.ID, &ct.Release{ArtifactIDs: []string{artifact.ID}})

	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: app.ID, Permissions: []string{"app:jobs:run"}}}}
	rec := s.runJobWithToken(c, app, tok, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"echo", "hi"},
		Partition: ct.PartitionTypeSystem,
		Meta: map[string]string{
			"keep":                      "yes",
			"flynn-system-app":          "true",
			"flynn-controller.app_name": "builder",
			"flynn-datastore":           "true",
			"flynn-plugin":              "true",
		},
	})
	c.Assert(rec.Code, Equals, 200)

	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	for _, j := range jobs {
		job := j.Job
		c.Assert(job.Partition, Not(Equals), "system")
		c.Assert(job.Metadata["flynn-system-app"], Equals, "")
		c.Assert(job.Metadata["flynn-controller.app_name"], Equals, app.Name)
		c.Assert(job.Metadata["keep"], Equals, "yes")
		c.Assert(job.Metadata["flynn-datastore"], Equals, "")
		c.Assert(job.Metadata["flynn-plugin"], Equals, "")
		c.Assert(job.Profiles, IsNil)
	}

	rec = s.runJobWithToken(c, app, tok, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"echo", "hi"},
		Profiles:  []host.JobProfile{host.JobProfileZFS},
	})
	c.Assert(rec.Code, Equals, 400)
}

func (s *S) TestRunJobClusterAdminCanSetSystemTrust(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-admin-trust"})
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hostID := fakeHostID()
	hc := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(hc)
	release := s.createTestRelease(c, app.ID, &ct.Release{ArtifactIDs: []string{artifact.ID}})

	res, err := s.c.RunJobDetached(app.ID, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"echo", "hi"},
		Partition: ct.PartitionTypeSystem,
		Profiles:  []host.JobProfile{host.JobProfileZFS},
		Meta:      map[string]string{"flynn-system-app": "true"},
	})
	c.Assert(err, IsNil)
	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	for _, j := range jobs {
		job := j.Job
		c.Assert(job.ID, Equals, res.ID)
		c.Assert(job.Partition, Equals, "system")
		c.Assert(job.Metadata["flynn-system-app"], Equals, "true")
		c.Assert(job.Profiles, DeepEquals, []host.JobProfile{host.JobProfileZFS})
	}
}

func (s *S) TestRunJobRejectsForeignRelease(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-own-release"})
	other := s.createTestApp(c, &ct.App{Name: "run-other-release"})
	foreign := s.createTestRelease(c, other.ID, &ct.Release{})
	_, err := s.c.RunJobDetached(app.ID, &ct.NewJob{
		ReleaseID: foreign.ID,
		Args:      []string{"echo", "hi"},
	})
	c.Assert(err, NotNil)
}

func (s *S) TestStartDetachedJobAppScopedCannotSetSystemTrust(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "start-untrusted"})
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hostID := fakeHostID()
	hc := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(hc)
	release := s.createTestRelease(c, app.ID, &ct.Release{ArtifactIDs: []string{artifact.ID}})

	_, err := s.api.startDetachedJob(app, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"echo", "hi"},
		Profiles:  []host.JobProfile{host.JobProfileKVM},
	})
	c.Assert(err, NotNil)

	res, err := s.api.startDetachedJob(app, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"echo", "hi"},
		Partition: ct.PartitionTypeSystem,
		Meta: map[string]string{
			"flynn-system-app":          "true",
			"flynn-controller.app_name": "builder",
			"keep":                      "yes",
		},
	})
	c.Assert(err, IsNil)
	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	for _, j := range jobs {
		job := j.Job
		c.Assert(job.ID, Equals, res.ID)
		c.Assert(job.Partition, Not(Equals), "system")
		c.Assert(job.Metadata["flynn-system-app"], Equals, "")
		c.Assert(job.Metadata["flynn-controller.app_name"], Equals, app.Name)
		c.Assert(job.Metadata["keep"], Equals, "yes")
		c.Assert(job.Profiles, IsNil)
	}
}

func (s *S) TestStartDetachedJobSystemAppKeepsTrust(c *C) {
	app := s.createTestApp(c, &ct.App{
		Name: "start-system-trust",
		Meta: map[string]string{"flynn-system-app": "true"},
	})
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hostID := fakeHostID()
	hc := tu.NewFakeHostClient(hostID, false)
	s.cc.AddHost(hc)
	release := s.createTestRelease(c, app.ID, &ct.Release{ArtifactIDs: []string{artifact.ID}})

	res, err := s.api.startDetachedJob(app, &ct.NewJob{
		ReleaseID: release.ID,
		Args:      []string{"/bin/taffy"},
		Partition: ct.PartitionTypeSystem,
		Profiles:  []host.JobProfile{host.JobProfileZFS},
		Meta:      map[string]string{"github": "true", "flynn-system-app": "true"},
	})
	c.Assert(err, IsNil)
	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	for _, j := range jobs {
		job := j.Job
		c.Assert(job.ID, Equals, res.ID)
		c.Assert(job.Partition, Equals, "system")
		c.Assert(job.Metadata["flynn-system-app"], Equals, "true")
		c.Assert(job.Metadata["github"], Equals, "true")
		c.Assert(job.Profiles, DeepEquals, []host.JobProfile{host.JobProfileZFS})
	}
}

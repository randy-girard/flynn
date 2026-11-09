package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	api "github.com/randy-girard/flynn/controller/api"
	ct "github.com/randy-girard/flynn/controller/types"
	logaggc "github.com/randy-girard/flynn/logaggregator/client"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/joboutput"
	"github.com/randy-girard/flynn/pkg/random"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) outputReq(c *C, method, path, body, bearer string) (int, string) {
	req, err := http.NewRequest(method, s.srv.URL+path, strings.NewReader(body))
	c.Assert(err, IsNil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		req.SetBasicAuth("", authKey)
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "text/plain")
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(b)
}

func (s *S) appBearer(c *C, app *ct.App, perms ...string) string {
	now := time.Now()
	tok, err := s.tokenSigner.Sign(&api.AccessToken{
		UserEmail:  "scoped@flynn.test",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		AppGrants: []*api.AppGrant{
			{AppId: app.ID, Permissions: perms},
			{AppId: app.Name, Permissions: perms},
		},
	})
	c.Assert(err, IsNil)
	return tok
}

func (s *S) TestJobOutputStoredWhenBuilderStops(c *C) {
	s.api.archiveOutputSync = true
	defer func() { s.api.archiveOutputSync = false }()

	app := s.createTestApp(c, &ct.App{Name: "job-output-builder"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	uuid := random.UUID()
	clusterID := cluster.GenerateJobID("host1", uuid)
	s.flac.logs[app.ID] = []logaggc.Message{
		{JobID: clusterID, Msg: "-----> Building app", ProcessType: "slugbuilder", Stream: "stdout"},
		{JobID: clusterID, Msg: "compiled slug", ProcessType: "slugbuilder", Stream: "stdout"},
	}

	job := &ct.Job{
		ID: clusterID, UUID: uuid, HostID: "host1",
		AppID: app.ID, ReleaseID: release.ID,
		Type: "slugbuilder", State: ct.JobStatePending,
	}
	c.Assert(s.c.PutJob(job), IsNil)
	code, body := s.outputReq(c, "GET", "/apps/"+app.ID+"/jobs/"+uuid+"/output", "", "")
	c.Assert(code, Equals, 404)
	c.Assert(body, Equals, "")

	job.State = ct.JobStateDown
	c.Assert(s.c.PutJob(job), IsNil)
	code, body = s.outputReq(c, "GET", "/apps/"+app.ID+"/jobs/"+uuid+"/output", "", "")
	c.Assert(code, Equals, 200)
	c.Assert(body, Equals, "-----> Building app\ncompiled slug\n")

	var stored []byte
	c.Assert(s.hc.db.QueryRow(`SELECT body FROM job_output WHERE job_id = $1`, uuid).Scan(&stored), IsNil)
	c.Assert(bytes.HasPrefix(stored, []byte{0x1f, 0x8b}), Equals, true)
	plain, err := joboutput.Decompress(stored)
	c.Assert(err, IsNil)
	c.Assert(string(plain), Equals, "-----> Building app\ncompiled slug\n")

	list, err := s.c.JobList(app.ID)
	c.Assert(err, IsNil)
	raw, err := json.Marshal(list)
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(string(raw), "compiled slug"), Equals, false)

	s.flac.logs[app.ID] = []logaggc.Message{
		{JobID: clusterID, Msg: "x", Stream: "stdout"},
	}
	c.Assert(s.c.PutJob(job), IsNil)
	code, body = s.outputReq(c, "GET", "/apps/"+app.ID+"/jobs/"+uuid+"/output", "", "")
	c.Assert(code, Equals, 200)
	c.Assert(body, Equals, "-----> Building app\ncompiled slug\n")
}

func (s *S) TestJobOutputUsesUUIDWhenClusterIDHasNoLines(c *C) {
	s.api.archiveOutputSync = true
	defer func() { s.api.archiveOutputSync = false }()

	app := s.createTestApp(c, &ct.App{Name: "job-output-uuid"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	uuid := random.UUID()
	clusterID := cluster.GenerateJobID("host1", uuid)
	s.flac.logs[app.ID] = []logaggc.Message{
		{JobID: uuid, Msg: "release command ok", ProcessType: "release", Stream: "stdout"},
	}
	c.Assert(s.c.PutJob(&ct.Job{
		ID: clusterID, UUID: uuid, HostID: "host1",
		AppID: app.ID, ReleaseID: release.ID,
		Type: "release", State: ct.JobStateDown,
	}), IsNil)
	code, body := s.outputReq(c, "GET", "/apps/"+app.ID+"/jobs/"+uuid+"/output", "", "")
	c.Assert(code, Equals, 200)
	c.Assert(body, Equals, "release command ok\n")
}

func (s *S) TestWebJobOutputIsNotStored(c *C) {
	s.api.archiveOutputSync = true
	defer func() { s.api.archiveOutputSync = false }()

	app := s.createTestApp(c, &ct.App{Name: "job-output-web"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})
	uuid := random.UUID()
	s.flac.logs[app.ID] = []logaggc.Message{
		{JobID: uuid, Msg: "web request", ProcessType: "web", Stream: "stdout"},
	}
	s.createTestJob(c, &ct.Job{
		UUID: uuid, AppID: app.ID, ReleaseID: release.ID,
		Type: "web", State: ct.JobStateDown,
	})
	code, body := s.outputReq(c, "GET", "/apps/"+app.ID+"/jobs/"+uuid+"/output", "", "")
	c.Assert(code, Equals, 404)
	c.Assert(body, Equals, "")
}

func (s *S) TestReleaseOutputRoundTrip(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "release-output"})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Env: map[string]string{"TOKEN": "supersecret-value"},
	})
	path := "/apps/" + app.ID + "/releases/" + release.ID + "/output"

	code, _ := s.outputReq(c, "PUT", path, " \n", "")
	c.Assert(code, Equals, 200)
	code, body := s.outputReq(c, "GET", path, "", "")
	c.Assert(code, Equals, 404)

	long := "-----> Creating release\n=====> Application deployed\n"
	code, _ = s.outputReq(c, "PUT", path, long, "")
	c.Assert(code, Equals, 200)
	code, _ = s.outputReq(c, "PUT", path, "no\n", "")
	c.Assert(code, Equals, 200)
	code, body = s.outputReq(c, "GET", path, "", "")
	c.Assert(code, Equals, 200)
	c.Assert(body, Equals, long)

	longer := long + "using supersecret-value\n"
	code, _ = s.outputReq(c, "PUT", path, longer, "")
	c.Assert(code, Equals, 200)
	code, body = s.outputReq(c, "GET", path, "", "")
	c.Assert(code, Equals, 200)
	c.Assert(body, Equals, longer)

	var stored []byte
	c.Assert(s.hc.db.QueryRow(`SELECT body FROM release_output WHERE release_id = $1`, release.ID).Scan(&stored), IsNil)
	c.Assert(bytes.HasPrefix(stored, []byte{0x1f, 0x8b}), Equals, true)

	other := s.createTestApp(c, &ct.App{Name: "release-output-other"})
	code, _ = s.outputReq(c, "GET", "/apps/"+other.ID+"/releases/"+release.ID+"/output", "", "")
	c.Assert(code, Equals, 404)

	bearer := s.appBearer(c, app, "app:logs:read")
	code, body = s.outputReq(c, "GET", path, "", bearer)
	c.Assert(code, Equals, 200)
	c.Assert(strings.Contains(body, "supersecret-value"), Equals, false)
	c.Assert(strings.Contains(body, "[redacted]"), Equals, true)
	c.Assert(strings.Contains(body, "Application deployed"), Equals, true)
}

func (s *S) TestJobOutputRedactsReleaseEnv(c *C) {
	s.api.archiveOutputSync = true
	defer func() { s.api.archiveOutputSync = false }()

	app := s.createTestApp(c, &ct.App{Name: "job-output-redact"})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Env: map[string]string{"TOKEN": "supersecret-value"},
	})
	uuid := random.UUID()
	s.flac.logs[app.ID] = []logaggc.Message{
		{JobID: uuid, Msg: "token supersecret-value", ProcessType: "postdeploy", Stream: "stdout"},
	}
	c.Assert(s.c.PutJob(&ct.Job{
		UUID: uuid, AppID: app.ID, ReleaseID: release.ID,
		Type: "postdeploy", State: ct.JobStateDown,
	}), IsNil)

	path := "/apps/" + app.ID + "/jobs/" + uuid + "/output"
	code, body := s.outputReq(c, "GET", path, "", "")
	c.Assert(code, Equals, 200)
	c.Assert(body, Equals, "token supersecret-value\n")

	bearer := s.appBearer(c, app, "app:logs:read")
	code, body = s.outputReq(c, "GET", path, "", bearer)
	c.Assert(code, Equals, 200)
	c.Assert(strings.Contains(body, "supersecret-value"), Equals, false)
	c.Assert(strings.Contains(body, "[redacted]"), Equals, true)
}

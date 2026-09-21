package data

import (
	ct "github.com/randy-girard/flynn/controller/types"

	. "github.com/flynn/go-check"
)

// A destroyed volume must not abort job persistence. job_volumes has a foreign
// key to volumes, and PutJob used to fail with SQLSTATE 23503. The scheduler
// retries that as unknown_error and will not publish the scale-request
// complete event while the job is still queued, so a one-by-one deploy waits
// out its timeout even after the process has already exited.
func (s *S) TestJobAddSkipsMissingVolume(c *C) {
	appID := "11111111-1111-4111-8111-111111111111"
	releaseID := "22222222-2222-4222-8222-222222222222"
	missingVol := "33333333-3333-4333-8333-333333333333"
	presentVol := "44444444-4444-4444-8444-444444444444"
	jobUUID := "55555555-5555-4555-8555-555555555555"

	c.Assert(s.db.Exec(
		"INSERT INTO apps (app_id, name) VALUES ($1, $2)",
		appID, "job-missing-vol",
	), IsNil)
	c.Assert(s.db.Exec(
		"INSERT INTO releases (release_id, app_id) VALUES ($1, $2)",
		releaseID, appID,
	), IsNil)
	c.Assert(s.db.Exec(
		"UPDATE apps SET release_id = $2 WHERE app_id = $1",
		appID, releaseID,
	), IsNil)
	c.Assert(s.db.Exec(
		`INSERT INTO volumes (volume_id, host_id, type, state, app_id, release_id)
		 VALUES ($1, 'node1', 'data', 'created', $2, $3)`,
		presentVol, appID, releaseID,
	), IsNil)

	repo := NewJobRepo(s.db)
	job := &ct.Job{
		ID:        "node1-" + jobUUID,
		UUID:      jobUUID,
		HostID:    "node1",
		AppID:     appID,
		ReleaseID: releaseID,
		Type:      "web",
		State:     ct.JobStateUp,
		Meta:      map[string]string{},
		VolumeIDs: []string{missingVol, presentVol},
	}
	if err := repo.Add(job); err != nil {
		c.Fatalf("job persist must survive a deleted volume (job_volumes FK 23503 blocked scale completion): %v", err)
	}

	got, err := repo.Get(jobUUID)
	c.Assert(err, IsNil)
	c.Assert(string(got.State), Equals, string(ct.JobStateUp))
	c.Assert(got.VolumeIDs, DeepEquals, []string{presentVol})

	job.State = ct.JobStateDown
	c.Assert(repo.Add(job), IsNil)
	got, err = repo.Get(jobUUID)
	c.Assert(err, IsNil)
	c.Assert(string(got.State), Equals, string(ct.JobStateDown))
	c.Assert(got.VolumeIDs, DeepEquals, []string{presentVol})
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
)

func (s *S) TestTenantBackupLock(c *C) {
	providerID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	resourceID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	otherID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	_ = s.hc.db.Exec(`DELETE FROM resources WHERE resource_id IN ($1, $2)`, resourceID, otherID)
	_ = s.hc.db.Exec(`DELETE FROM providers WHERE provider_id = $1`, providerID)
	c.Assert(s.hc.db.Exec(`INSERT INTO providers (provider_id, name, url) VALUES ($1, $2, $3)`,
		providerID, "tenant-backup-test", "http://tenant-backup-test.example"), IsNil)
	c.Assert(s.hc.db.Exec(`INSERT INTO resources (resource_id, provider_id, external_id, env) VALUES ($1, $2, 'ext-1', '{}')`,
		resourceID, providerID), IsNil)
	c.Assert(s.hc.db.Exec(`INSERT INTO resources (resource_id, provider_id, external_id, env) VALUES ($1, $2, 'ext-2', '{}')`,
		otherID, providerID), IsNil)
	defer func() {
		_ = s.hc.db.Exec(`DELETE FROM resources WHERE provider_id = $1`, providerID)
		_ = s.hc.db.Exec(`DELETE FROM providers WHERE provider_id = $1`, providerID)
	}()

	res := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/begin", `{"timestamp":"20261111T160405Z"}`)
	c.Assert(res.Code, Equals, 200)
	var began ct.TenantBackupBegin
	c.Assert(json.Unmarshal(res.Body, &began), IsNil)
	c.Assert(began.Started, Equals, true)
	c.Assert(began.Status.AttemptState, Equals, "running")

	again := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/begin", `{"timestamp":"20261111T160406Z"}`)
	c.Assert(again.Code, Equals, http.StatusConflict)
	var busy ct.TenantBackupBegin
	c.Assert(json.Unmarshal(again.Body, &busy), IsNil)
	c.Assert(busy.Started, Equals, false)
	c.Assert(busy.Status.AttemptTimestamp, Equals, "20261111T160405Z")

	beat := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/heartbeat", `{"timestamp":"20261111T160405Z"}`)
	c.Assert(beat.Code, Equals, 200)

	c.Assert(s.api.tenantBackupRepo.TouchHeartbeat(resourceID, time.Now().Add(-3*time.Minute)), IsNil)
	taken := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/begin", `{"timestamp":"20261111T170000Z"}`)
	c.Assert(taken.Code, Equals, 200)
	var took ct.TenantBackupBegin
	c.Assert(json.Unmarshal(taken.Body, &took), IsNil)
	c.Assert(took.Started, Equals, true)
	c.Assert(took.Status.AttemptTimestamp, Equals, "20261111T170000Z")

	failed := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/finish", `{"state":"failed","error":"pg_dump failed","timestamp":"20261111T170000Z"}`)
	c.Assert(failed.Code, Equals, 200)
	dumps := s.backupJSON(c, http.MethodGet, "/tenant-backups/"+resourceID+"/dumps", "")
	c.Assert(dumps.Code, Equals, 200)
	c.Assert(string(dumps.Body), Equals, "[]")

	okBegin := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/begin", `{"timestamp":"20261111T180000Z"}`)
	c.Assert(okBegin.Code, Equals, 200)
	done := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/finish", `{"state":"succeeded","timestamp":"20261111T180000Z","databases":["appdb","shop"]}`)
	c.Assert(done.Code, Equals, 200)
	var success ct.TenantBackup
	c.Assert(json.Unmarshal(done.Body, &success), IsNil)
	c.Assert(success.LastSuccessTimestamp, Equals, "20261111T180000Z")

	listed := s.backupJSON(c, http.MethodGet, "/tenant-backups/"+resourceID+"/dumps", "")
	c.Assert(listed.Code, Equals, 200)
	var dumpRows []ct.TenantBackupDump
	c.Assert(json.Unmarshal(listed.Body, &dumpRows), IsNil)
	c.Assert(dumpRows, HasLen, 1)
	c.Assert(dumpRows[0].Timestamp, Equals, "20261111T180000Z")
	c.Assert(strings.Join(dumpRows[0].Databases, ","), Equals, "appdb,shop")

	fresh := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/begin", `{"timestamp":"20261111T190000Z"}`)
	c.Assert(fresh.Code, Equals, 200)
	refused := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/fail", `{"error":"tenant dumps are not stored in the platform postgres blobstore"}`)
	c.Assert(refused.Code, Equals, 200)
	var still ct.TenantBackup
	c.Assert(json.Unmarshal(refused.Body, &still), IsNil)
	c.Assert(still.AttemptState, Equals, "running")
	c.Assert(still.LastSuccessTimestamp, Equals, "20261111T180000Z")

	bad := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/begin", `{"timestamp":"../etc"}`)
	c.Assert(bad.Code, Equals, 400)
	reserved := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+resourceID+"/finish", `{"state":"succeeded","timestamp":"20261111T190000Z","databases":["postgres"]}`)
	c.Assert(reserved.Code, Equals, 400)

	other := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+otherID+"/begin", `{"timestamp":"20261112T000000Z"}`)
	c.Assert(other.Code, Equals, 200)
	otherDone := s.backupJSON(c, http.MethodPost, "/tenant-backups/"+otherID+"/finish", `{"state":"succeeded","timestamp":"20261112T000000Z","databases":["otherdb"]}`)
	c.Assert(otherDone.Code, Equals, 200)
	otherDumps := s.backupJSON(c, http.MethodGet, "/tenant-backups/"+otherID+"/dumps", "")
	body, err := io.ReadAll(strings.NewReader(string(otherDumps.Body)))
	c.Assert(err, IsNil)
	if strings.Contains(string(body), "appdb") || strings.Contains(string(body), resourceID) {
		c.Fatalf("resources shared a catalog: %s", body)
	}

	c.Assert(s.hc.db.Exec(`DELETE FROM tenant_backup_dumps WHERE resource_id = $1 AND dump_timestamp = $2`, resourceID, "20261111T180000Z"), IsNil)
	left, err := s.api.tenantBackupRepo.ListDumps(resourceID)
	c.Assert(err, IsNil)
	c.Assert(left, HasLen, 0)
	kept, err := data.NewTenantBackupRepo(s.hc.db).Get(resourceID)
	c.Assert(err, IsNil)
	c.Assert(kept.LastSuccessTimestamp, Equals, "20261111T180000Z")
}

type backupResponse struct {
	Code int
	Body []byte
}

func (s *S) backupJSON(c *C, method, path, body string) backupResponse {
	req, err := http.NewRequest(method, s.srv.URL+path, strings.NewReader(body))
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return backupResponse{Code: res.StatusCode, Body: raw}
}

package backup

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

type backupStub struct {
	apps       map[string]*ct.App
	releases   map[string]*ct.Release
	formations map[string]*ct.Formation
	expanded   map[string]*ct.ExpandedFormation
	err        map[string]error
}

func (s backupStub) GetApp(name string) (*ct.App, error) {
	if err, ok := s.err[name]; ok {
		return nil, err
	}
	app, ok := s.apps[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return app, nil
}
func (s backupStub) GetAppRelease(id string) (*ct.Release, error) {
	r, ok := s.releases[id]
	if !ok {
		return nil, errors.New("no release")
	}
	return r, nil
}
func (s backupStub) GetFormation(appID, releaseID string) (*ct.Formation, error) {
	f, ok := s.formations[appID+"/"+releaseID]
	if !ok {
		return nil, ct.ErrNotFound
	}
	return f, nil
}

func (s backupStub) FormationList(appID string) ([]*ct.Formation, error) {
	if err, ok := s.err["formations:"+appID]; ok {
		return nil, err
	}
	var out []*ct.Formation
	prefix := appID + "/"
	for key, f := range s.formations {
		if strings.HasPrefix(key, prefix) && f != nil {
			item := *f
			item.AppID = appID
			item.ReleaseID = strings.TrimPrefix(key, prefix)
			out = append(out, &item)
		}
	}
	return out, nil
}

func (s backupStub) GetExpandedFormation(appID, releaseID string) (*ct.ExpandedFormation, error) {
	if s.expanded != nil {
		if ef, ok := s.expanded[appID+"/"+releaseID]; ok {
			return ef, nil
		}
		if ef, ok := s.expanded[appID]; ok {
			return ef, nil
		}
	}
	app, err := s.GetApp(appID)
	if err != nil {
		return nil, err
	}
	release, err := s.GetAppRelease(app.ID)
	if err != nil {
		return nil, err
	}
	formation, err := s.GetFormation(app.ID, release.ID)
	if err != nil {
		return nil, err
	}
	return &ct.ExpandedFormation{
		App:       app,
		Release:   release,
		Processes: formation.Processes,
	}, nil
}

func requiredBackupApps() backupStub {
	apps := map[string]*ct.App{}
	releases := map[string]*ct.Release{}
	formations := map[string]*ct.Formation{}
	for _, name := range []string{"postgres", "discoverd", "flannel", "controller"} {
		apps[name] = &ct.App{ID: name + "-id", Name: name}
		releases[name+"-id"] = &ct.Release{ID: name + "-rel", Env: map[string]string{"PGHOST": "h"}}
		formations[name+"-id/"+name+"-rel"] = &ct.Formation{Processes: map[string]int{name: 1}}
	}
	return backupStub{apps: apps, releases: releases, formations: formations, err: map[string]error{}}
}

func TestGetAppsRequiresCoreAndSkipsOptional(t *testing.T) {
	stub := requiredBackupApps()
	data, err := getApps(stub)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"postgres", "discoverd", "flannel", "controller"} {
		if data[name] == nil || data[name].Release == nil {
			t.Fatalf("missing required %s", name)
		}
	}
	if _, ok := data["mariadb"]; ok {
		t.Fatal("plugin apps must not be in the required backup set")
	}
}

func TestGetAppsFailsClosedWithoutPostgres(t *testing.T) {
	stub := requiredBackupApps()
	stub.err["postgres"] = errors.New("gone")
	if _, err := getApps(stub); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("got %v", err)
	}
}

func TestGetAppsFailsOnMissingRelease(t *testing.T) {
	stub := requiredBackupApps()
	stub.releases = map[string]*ct.Release{}
	if _, err := getApps(stub); err == nil || !strings.Contains(err.Error(), "release") {
		t.Fatalf("got %v", err)
	}
}

func TestGetAppsFallbackWhenCurrentFormationMissing(t *testing.T) {
	stub := requiredBackupApps()
	delete(stub.formations, "postgres-id/postgres-rel")
	stub.formations["postgres-id/old-rel"] = &ct.Formation{Processes: map[string]int{"postgres": 1, "web": 1}}
	data, err := getApps(stub)
	if err != nil {
		t.Fatal(err)
	}
	if data["postgres"].Processes["postgres"] != 1 {
		t.Fatalf("processes=%v", data["postgres"].Processes)
	}
}

func TestGetAppsEmptyFormationWhenNoneExist(t *testing.T) {
	stub := requiredBackupApps()
	delete(stub.formations, "postgres-id/postgres-rel")
	data, err := getApps(stub)
	if err != nil {
		t.Fatal(err)
	}
	if data["postgres"] == nil || data["postgres"].Release == nil {
		t.Fatal("postgres backup must continue without a formation row")
	}
}

type dumpJobLister struct {
	jobs map[string][]*ct.Job
	err  error
}

func (s dumpJobLister) JobList(appID string) ([]*ct.Job, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.jobs[appID], nil
}

func mariadbDumpFormation(scale int) *ct.ExpandedFormation {
	return &ct.ExpandedFormation{
		Release:   &ct.Release{ID: "rel", Env: map[string]string{"MYSQL_PWD": "x"}},
		Processes: map[string]int{"mariadb": scale},
	}
}

func TestShouldDumpPlugin(t *testing.T) {
	p := plugin.Installed{Name: "mariadb"}
	spec := &plugin.BackupSpec{File: "mysql.sql.gz", Process: "mariadb", RequireScale: true}
	lister := dumpJobLister{jobs: map[string][]*ct.Job{}}

	if shouldDumpPlugin(lister, p, nil, mariadbDumpFormation(1)) {
		t.Fatal("nil spec must skip")
	}
	if shouldDumpPlugin(lister, p, spec, nil) {
		t.Fatal("nil formation must skip")
	}

	always := &plugin.BackupSpec{File: "dump", RequireScale: false}
	if !shouldDumpPlugin(lister, p, always, mariadbDumpFormation(0)) {
		t.Fatal("RequireScale=false must dump even at scale 0")
	}
	if !shouldDumpPlugin(lister, p, spec, mariadbDumpFormation(1)) {
		t.Fatal("desired scale > 0 must dump")
	}
	if shouldDumpPlugin(lister, p, spec, mariadbDumpFormation(0)) {
		t.Fatal("scale 0 with no jobs must skip")
	}

	up := dumpJobLister{jobs: map[string][]*ct.Job{
		"mariadb": {{Type: "mariadb", State: ct.JobStateUp}},
	}}
	if !shouldDumpPlugin(up, p, spec, mariadbDumpFormation(0)) {
		t.Fatal("running dump process must dump even when desired scale is 0")
	}

	webOnly := dumpJobLister{jobs: map[string][]*ct.Job{
		"mariadb": {{Type: "web", State: ct.JobStateUp}},
	}}
	if shouldDumpPlugin(webOnly, p, spec, mariadbDumpFormation(0)) {
		t.Fatal("web-only jobs must not dump the appliance process")
	}

	fail := dumpJobLister{err: errors.New("controller down")}
	if shouldDumpPlugin(fail, p, spec, mariadbDumpFormation(0)) {
		t.Fatal("JobList error must skip rather than dump")
	}

	unnamed := &plugin.BackupSpec{File: "mysql.sql.gz", RequireScale: true}
	namedJobs := dumpJobLister{jobs: map[string][]*ct.Job{
		"mariadb": {{Type: "mariadb", State: ct.JobStateUp}},
	}}
	if !shouldDumpPlugin(namedJobs, p, unnamed, mariadbDumpFormation(0)) {
		t.Fatal("empty Process must fall back to plugin name")
	}

	starting := dumpJobLister{jobs: map[string][]*ct.Job{
		"mariadb": {{Type: "mariadb", State: ct.JobStateStarting}},
	}}
	if !shouldDumpPlugin(starting, p, spec, mariadbDumpFormation(0)) {
		t.Fatal("Starting dump process must still dump")
	}

	withNil := dumpJobLister{jobs: map[string][]*ct.Job{
		"mariadb": {nil, {Type: "mariadb", State: ct.JobStateDown}},
	}}
	if shouldDumpPlugin(withNil, p, spec, mariadbDumpFormation(0)) {
		t.Fatal("nil/down jobs must skip")
	}
}

func TestIncludePluginFormationsCopiesArtifacts(t *testing.T) {
	stub := requiredBackupApps()
	art := &ct.Artifact{ID: "img-1", URI: "http://blobstore.discoverd/mongodb.squashfs"}
	stub.apps["mongodb"] = &ct.App{ID: "mongodb-id", Name: "mongodb"}
	stub.releases["mongodb-id"] = &ct.Release{ID: "mongodb-rel"}
	stub.expanded = map[string]*ct.ExpandedFormation{
		"mongodb-id/mongodb-rel": {
			App:       stub.apps["mongodb"],
			Release:   stub.releases["mongodb-id"],
			Artifacts: []*ct.Artifact{art},
			Processes: map[string]int{"mongodb": 1},
		},
	}
	data := map[string]*ct.ExpandedFormation{}
	if err := includePluginFormations(stub, data, []plugin.Installed{{Name: "mongodb"}}); err != nil {
		t.Fatal(err)
	}
	got := data["mongodb"]
	if got == nil || len(got.Artifacts) != 1 || got.Artifacts[0].URI != art.URI {
		t.Fatalf("plugin backup must include blobstore artifacts, got %+v", got)
	}
	if got.DeprecatedImageArtifact == nil || got.DeprecatedImageArtifact.URI != art.URI {
		t.Fatal("plugin backup must set deprecated artifact for restore compatibility")
	}
}

func TestIncludePluginFormationsSkipsMissingAndErrorsOnRelease(t *testing.T) {
	stub := requiredBackupApps()
	data := map[string]*ct.ExpandedFormation{}
	if err := includePluginFormations(stub, data, []plugin.Installed{{Name: "nope"}}); err != nil {
		t.Fatal(err)
	}
	if data["nope"] != nil {
		t.Fatal("missing plugin app must be skipped")
	}

	stub.apps["mongodb"] = &ct.App{ID: "mongodb-id", Name: "mongodb"}
	if err := includePluginFormations(stub, data, []plugin.Installed{{Name: "mongodb"}}); err == nil {
		t.Fatal("missing release must fail closed")
	}

	data["mongodb"] = &ct.ExpandedFormation{App: &ct.App{Name: "keep"}}
	stub.releases["mongodb-id"] = &ct.Release{ID: "mongodb-rel"}
	stub.expanded = map[string]*ct.ExpandedFormation{
		"mongodb-id/mongodb-rel": {App: stub.apps["mongodb"], Release: stub.releases["mongodb-id"]},
	}
	if err := includePluginFormations(stub, data, []plugin.Installed{{Name: "mongodb"}}); err != nil {
		t.Fatal(err)
	}
	if data["mongodb"].App.Name != "keep" {
		t.Fatal("must not overwrite an existing formation")
	}
}

func TestTarWriterWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTarWriter("flynn-backup-test", &buf, nil)
	if err := tw.WriteJSON("flynn.json", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(&buf)
	hdr, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hdr.Name, "flynn.json") {
		t.Fatalf("name=%s", hdr.Name)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"k": "v"`) {
		t.Fatalf("%s", body)
	}
}

package backup

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

type backupStub struct {
	apps       map[string]*ct.App
	releases   map[string]*ct.Release
	formations map[string]*ct.Formation
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
		return nil, errors.New("no formation")
	}
	return f, nil
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

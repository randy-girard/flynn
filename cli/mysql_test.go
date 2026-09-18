package main

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestGetMysqlRunConfig(t *testing.T) {
	if _, err := getMysqlRunConfig(fakeRedisReleaseClient{}, "demo", &ct.Release{Env: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "No mysql database") {
		t.Fatalf("missing resource: %v", err)
	}

	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{}}
	appRel := &ct.Release{Env: map[string]string{"FLYNN_MYSQL": "mysql-abc", "MYSQL_USER": "u"}}
	if _, err := getMysqlRunConfig(client, "demo", appRel); err == nil || !strings.Contains(err.Error(), "error getting mysql release") {
		t.Fatalf("missing release: %v", err)
	}

	client = fakeRedisReleaseClient{releases: map[string]*ct.Release{"mysql-abc": {ID: "rel"}}}
	if _, err := getMysqlRunConfig(client, "demo", &ct.Release{Env: map[string]string{"FLYNN_MYSQL": "mysql-abc"}}); err == nil || !strings.Contains(err.Error(), "MYSQL_USER") {
		t.Fatalf("missing user: %v", err)
	}

	appRel = &ct.Release{Env: map[string]string{
		"FLYNN_MYSQL":    "mysql-abc",
		"MYSQL_USER":     "u",
		"MYSQL_HOST":     "leader.mysql-abc.discoverd",
		"MYSQL_PWD":      "p",
		"MYSQL_DATABASE": "db",
	}}
	cfg, err := getMysqlRunConfig(client, "demo", appRel)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != "demo" || cfg.Release != "rel" || cfg.Env["MYSQL_DATABASE"] != "db" {
		t.Fatalf("%+v", cfg)
	}
	if _, err := getMysqlRunConfig(client, "demo", &ct.Release{Env: map[string]string{
		"FLYNN_MYSQL": "mysql-abc", "MYSQL_USER": "u", "MYSQL_HOST": "h", "MYSQL_PWD": "p",
	}}); err == nil || !strings.Contains(err.Error(), "MYSQL_DATABASE") {
		t.Fatalf("missing database: %v", err)
	}

	configMysqlDump(cfg)
	want := "mysqldump -h leader.mysql-abc.discoverd -u u db"
	if got := strings.Join(cfg.Args, " "); got != want {
		t.Fatalf("dump args %q", got)
	}
}

package main

import (
	"os"
	"strings"
	"testing"
)

func TestUpdaterSetsRedisApplianceStrategyBeforeDeploy(t *testing.T) {
	src, err := os.ReadFile("updater.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	ensure := strings.Index(body, "EnsureRedisApplianceStrategy")
	deploy := strings.Index(body, "deployApp(client, app, redisImage")
	if ensure < 0 || deploy < 0 {
		t.Fatal("updater must call EnsureRedisApplianceStrategy and deploy redis appliances")
	}
	if ensure > deploy {
		t.Fatal("strategy must be persisted before the redis appliance deploy")
	}
}

func TestUpdaterSkipsAppsWithNoRelease(t *testing.T) {
	src, err := os.ReadFile("updater.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	getRelease := strings.Index(body, "client.GetAppRelease(app.ID)")
	skip := strings.Index(body, "updaterdeploy.MissingAppReleaseSkip(app, err)")
	if getRelease < 0 || skip < 0 {
		t.Fatal("deployApp must skip GetAppRelease not-found for non-system apps")
	}
	if skip < getRelease {
		t.Fatal("missing-release skip must run after GetAppRelease")
	}
}

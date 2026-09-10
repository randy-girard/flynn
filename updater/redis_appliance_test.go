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

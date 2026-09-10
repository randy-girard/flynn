package main

import (
	"os"
	"strings"
	"testing"
)

func TestProvisionUsesNewRedisApplianceApp(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "ct.NewRedisApplianceApp(") {
		t.Fatal("redis-api must provision appliances via NewRedisApplianceApp (one-down-one-up + system meta)")
	}
	if strings.Contains(body, `Strategy: "all-at-once"`) {
		t.Fatal("redis-api must not hardcode all-at-once; that starts a new job while the old still holds /data")
	}
	if !strings.Contains(body, `Volumes: []ct.VolumeReq{{Path: "/data"}}`) {
		t.Fatal("redis process must request a persistent /data volume (DeleteOnStop must stay false)")
	}
}

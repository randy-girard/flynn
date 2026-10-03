package main

import (
	"os"
	"strings"
	"testing"
)

func TestSetEnvQueuesDeployWithoutWaiting(t *testing.T) {
	b, err := os.ReadFile("env.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "DeployAppRelease") {
		t.Fatal("env:set / env:unset must return after CreateDeployment; DeployAppRelease waits for the previous deploy")
	}
	if !strings.Contains(src, "CreateDeployment") || !strings.Contains(src, "HeadRelease") {
		t.Fatal("env changes must fork the queued head release and create a stacked deploy")
	}
}

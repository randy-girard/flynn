package main

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestChooseAppCreateStaysLocalWithoutALabel(t *testing.T) {
	local, name, err := chooseAppCreate("US", "", nil, nil)
	if err != nil || !local || name != "" {
		t.Fatalf("omitted region creates on this controller: local=%v name=%q err=%v", local, name, err)
	}
	local, name, err = chooseAppCreate("US", "US", nil, nil)
	if err != nil || !local || name != "" {
		t.Fatalf("this cluster's region stays here: local=%v name=%q err=%v", local, name, err)
	}
}

func TestChooseAppCreateDoesNotLabelTheLocalController(t *testing.T) {
	links := []ct.RegionLink{{
		Region:        "EU",
		Domain:        "eu.example.com",
		ControllerURL: "https://controller.eu.example.com",
		DashboardURL:  "https://dashboard.eu.example.com",
	}}
	clusters := []appCreateCluster{{
		Name:          "eu",
		ControllerURL: "https://controller.eu.example.com",
		LoggedIn:      true,
	}}
	local, name, err := chooseAppCreate("US", "EU", links, clusters)
	if err != nil || local || name != "eu" {
		t.Fatalf("create must run on the linked cluster: local=%v name=%q err=%v", local, name, err)
	}
	app := &ct.App{Name: "shop"}
	if app.Meta["region"] != "" {
		t.Fatal("apps:create must not label the app with the other region")
	}
}

func TestChooseAppCreateRequiresFlynnrcLogin(t *testing.T) {
	links := []ct.RegionLink{{
		Region:        "EU",
		Domain:        "eu.example.com",
		ControllerURL: "https://controller.eu.example.com",
	}}
	_, _, err := chooseAppCreate("US", "EU", links, nil)
	if err == nil || !strings.Contains(err.Error(), "flynn cluster:add") || !strings.Contains(err.Error(), "flynn login") {
		t.Fatalf("missing flynnrc: %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Fatalf("must not ask to store a password on the controller: %v", err)
	}
	clusters := []appCreateCluster{{
		Name:          "eu",
		ControllerURL: "https://controller.eu.example.com",
		LoggedIn:      false,
	}}
	_, _, err = chooseAppCreate("US", "EU", links, clusters)
	if err == nil || !strings.Contains(err.Error(), "flynn login") {
		t.Fatalf("missing login: %v", err)
	}
}

func TestAppsCreateUsageHasRegion(t *testing.T) {
	if !strings.Contains(commands["apps:create"].usage, "--region") {
		t.Fatal("apps:create must accept --region")
	}
}

package main

import (
	"reflect"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
)

func TestRouteServiceMatchesApp(t *testing.T) {
	webRelease := &ct.Release{Processes: map[string]ct.ProcessType{
		"web": {Service: "shop-web"},
	}}
	customRelease := &ct.Release{Processes: map[string]ct.ProcessType{
		"api": {Service: "shop-internal"},
		"web": {Ports: []ct.Port{{Service: &host.Service{Name: "shop-from-port"}}}},
	}}

	cases := []struct {
		name    string
		app     string
		service string
		release *ct.Release
		want    bool
	}{
		{name: "default_web", app: "shop", service: "shop-web", want: true},
		{name: "admin_web_process", app: "shop", service: "shop-admin-web", want: true},
		{name: "hyphenated_app_web", app: "my-shop", service: "my-shop-web", want: true},
		{name: "tcp_custom_prefix", app: "shop", service: "shop-tcp", want: true},
		{name: "declared_process_service", app: "shop", service: "shop-internal", release: customRelease, want: true},
		{name: "declared_port_service", app: "shop", service: "shop-from-port", release: customRelease, want: true},
		{name: "release_web_service", app: "shop", service: "shop-web", release: webRelease, want: true},
		{name: "empty_service", app: "shop", service: "", want: false},
		{name: "empty_app", app: "", service: "shop-web", want: false},
		{name: "controller", app: "shop", service: "controller", want: false},
		{name: "blobstore", app: "shop", service: "blobstore", want: false},
		{name: "postgres_api", app: "shop", service: "postgres-api", want: false},
		{name: "dashboard_web", app: "shop", service: "dashboard-web", want: false},
		{name: "other_app_web", app: "shop", service: "other-web", want: false},
		{name: "app_name_without_hyphen", app: "shop", service: "shop", want: false},
		{name: "prefix_of_system_name", app: "c", service: "controller", want: false},
		{name: "declared_controller_bypass", app: "shop", service: "controller", release: &ct.Release{Processes: map[string]ct.ProcessType{"web": {Service: "controller"}}}, want: true},
		{name: "undeclared_custom", app: "shop", service: "random", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeServiceMatchesApp(tc.app, tc.service, tc.release)
			if got != tc.want {
				t.Fatalf("routeServiceMatchesApp(%q, %q) = %v, want %v", tc.app, tc.service, got, tc.want)
			}
		})
	}
}

func TestServiceOwnerNameCandidates(t *testing.T) {
	cases := []struct {
		service string
		want    []string
	}{
		{service: "", want: nil},
		{service: "controller", want: []string{"controller"}},
		{service: "shop-web", want: []string{"shop-web", "shop"}},
		{service: "my-shop-admin-web", want: []string{"my-shop-admin-web", "my-shop-admin", "my-shop", "my"}},
		{service: "postgres-api", want: []string{"postgres-api", "postgres"}},
		{service: "dashboard-web", want: []string{"dashboard-web", "dashboard"}},
		{service: "controller-web", want: []string{"controller-web", "controller"}},
	}
	for _, tc := range cases {
		got := serviceOwnerNameCandidates(tc.service)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("serviceOwnerNameCandidates(%q) = %v, want %v", tc.service, got, tc.want)
		}
	}
}

func TestPlatformAppOwnsService(t *testing.T) {
	owned := []string{"controller", "controller-web", "blobstore", "postgres", "postgres-api", "dashboard", "dashboard-web", "router-api", "status-web", "gitreceive"}
	for _, svc := range owned {
		if !platformAppOwnsService(svc) {
			t.Errorf("platformAppOwnsService(%q) = false, want true", svc)
		}
	}
	notOwned := []string{"shop-web", "shop", "www", "discovery", "my-controller"}
	for _, svc := range notOwned {
		if platformAppOwnsService(svc) {
			t.Errorf("platformAppOwnsService(%q) = true, want false", svc)
		}
	}
}

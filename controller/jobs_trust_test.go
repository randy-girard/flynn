package main

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
)

func scopedJobsRunToken(appID string) *authorizer.Token {
	return &authorizer.Token{
		AppGrants: []authorizer.AppGrant{{AppID: appID, Permissions: []string{"app:jobs:run"}}},
	}
}

func TestSanitizeOneOffJobAppScopedCannotEscalate(t *testing.T) {
	app := &ct.App{ID: "app-1", Name: "shop"}
	job := &ct.NewJob{
		Partition: ct.PartitionTypeSystem,
		Profiles:  []host.JobProfile{host.JobProfileZFS},
		Meta: map[string]string{
			"keep":                      "yes",
			"flynn-system-app":          "true",
			"flynn-controller.app_name": "builder",
			"flynn-controller.release":  "other",
			"flynn-datastore":           "true",
			"flynn-plugin":              "true",
		},
	}
	err := sanitizeOneOffJob(app, job, scopedJobsRunToken(app.ID))
	if err == nil {
		t.Fatal("expected profiles to be rejected")
	}
	if ve, ok := err.(ct.ValidationError); !ok || ve.Field != "profiles" {
		t.Fatalf("err = %v, want profiles validation error", err)
	}
	if job.Partition != ct.PartitionTypeUser {
		t.Fatalf("Partition = %q, want user", job.Partition)
	}
	if job.Meta["keep"] != "yes" {
		t.Fatalf("caller meta keep = %v", job.Meta)
	}
	for _, k := range []string{"flynn-system-app", "flynn-controller.app_name", "flynn-controller.release", "flynn-datastore", "flynn-plugin"} {
		if _, ok := job.Meta[k]; ok {
			t.Fatalf("reserved meta %q still present: %v", k, job.Meta)
		}
	}
}

func TestSanitizeOneOffJobAppScopedDropsSystemWithoutProfiles(t *testing.T) {
	app := &ct.App{ID: "app-1", Name: "shop"}
	job := &ct.NewJob{
		Partition: ct.PartitionTypeSystem,
		Meta:      map[string]string{"flynn-system-app": "true", "note": "ok"},
	}
	if err := sanitizeOneOffJob(app, job, scopedJobsRunToken(app.ID)); err != nil {
		t.Fatal(err)
	}
	if job.Partition != ct.PartitionTypeUser {
		t.Fatalf("Partition = %q, want user", job.Partition)
	}
	if _, ok := job.Meta["flynn-system-app"]; ok {
		t.Fatal("flynn-system-app must be dropped")
	}
	if job.Meta["note"] != "ok" {
		t.Fatalf("Meta = %v", job.Meta)
	}
}

func TestSanitizeOneOffJobKeepsBackgroundPartition(t *testing.T) {
	app := &ct.App{ID: "app-1"}
	job := &ct.NewJob{Partition: ct.PartitionTypeBackground}
	if err := sanitizeOneOffJob(app, job, scopedJobsRunToken(app.ID)); err != nil {
		t.Fatal(err)
	}
	if job.Partition != ct.PartitionTypeBackground {
		t.Fatalf("Partition = %q, want background", job.Partition)
	}
}

func TestSanitizeOneOffJobSystemAppKeepsTrust(t *testing.T) {
	app := &ct.App{
		ID:   "sys-1",
		Name: "blobstore",
		Meta: map[string]string{"flynn-system-app": "true"},
	}
	job := &ct.NewJob{
		Partition: ct.PartitionTypeSystem,
		Profiles:  []host.JobProfile{host.JobProfileKVM, host.JobProfileZFS},
		Meta:      map[string]string{"flynn-system-app": "true", "flynn-datastore": "true"},
	}
	if err := sanitizeOneOffJob(app, job, scopedJobsRunToken(app.ID)); err != nil {
		t.Fatal(err)
	}
	if job.Partition != ct.PartitionTypeSystem {
		t.Fatalf("Partition = %q, want system", job.Partition)
	}
	if len(job.Profiles) != 2 {
		t.Fatalf("Profiles = %v", job.Profiles)
	}
	if job.Meta["flynn-system-app"] != "true" || job.Meta["flynn-datastore"] != "true" {
		t.Fatalf("Meta = %v", job.Meta)
	}
}

func TestSanitizeOneOffJobClusterAdminKeepsTrust(t *testing.T) {
	app := &ct.App{ID: "app-1", Name: "shop"}
	job := &ct.NewJob{
		Partition: ct.PartitionTypeSystem,
		Profiles:  []host.JobProfile{host.JobProfileLoop},
		Meta:      map[string]string{"flynn-system-app": "true"},
	}
	admin := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	if err := sanitizeOneOffJob(app, job, admin); err != nil {
		t.Fatal(err)
	}
	if job.Partition != ct.PartitionTypeSystem || len(job.Profiles) != 1 || job.Meta["flynn-system-app"] != "true" {
		t.Fatalf("admin job stripped: %+v", job)
	}
}

func TestSanitizeOneOffJobNilTokenIsUntrusted(t *testing.T) {
	app := &ct.App{ID: "app-1"}
	job := &ct.NewJob{Partition: ct.PartitionTypeSystem}
	if err := sanitizeOneOffJob(app, job, nil); err != nil {
		t.Fatal(err)
	}
	if job.Partition != ct.PartitionTypeUser {
		t.Fatalf("nil token Partition = %q, want user", job.Partition)
	}
}

func TestReleaseOwnedByApp(t *testing.T) {
	app := &ct.App{ID: "app-1"}
	if err := releaseOwnedByApp(&ct.Release{AppID: "app-1"}, app); err != nil {
		t.Fatal(err)
	}
	if err := releaseOwnedByApp(&ct.Release{}, app); err != nil {
		t.Fatal(err)
	}
	err := releaseOwnedByApp(&ct.Release{AppID: "app-2"}, app)
	if err == nil {
		t.Fatal("expected foreign release to be rejected")
	}
	if ve, ok := err.(ct.ValidationError); !ok || ve.Field != "release" {
		t.Fatalf("err = %v, want release validation error", err)
	}
}

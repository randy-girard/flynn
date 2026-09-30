package main

import (
	"net/http"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

func TestRefuseDatastoreRunJob(t *testing.T) {
	t.Setenv("FLYNN_HOST_AUTH_KEY", "host-secret")
	store := &ct.App{Name: "pg-isolated", Meta: map[string]string{"flynn-system-app": "true", "flynn-datastore": "true"}}
	user := &ct.App{Name: "shop"}
	platform := &ct.App{Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}}
	redis := ct.NewRedisApplianceApp("redis-11111111-2222-3333-4444-555555555555")

	if err := refuseDatastoreRunJob(user, &http.Request{}); err != nil {
		t.Fatalf("user app: %v", err)
	}

	req := &http.Request{Header: http.Header{}}
	system := map[string]string{"flynn-system-app": "true"}
	stores := []*ct.App{store, platform, redis}
	for _, name := range []string{
		"mysql-plugin", "mysql-amber-abcdef", "mariadb",
		"mongodb-plugin", "mongodb-grove-xyzxyz",
		"kafka-plugin", "kafka-delta-abcdef",
		"clickhouse-plugin", "clickhouse-fjord-abcdef",
		"postgres-plugin", "pg-amber-abcdef",
	} {
		stores = append(stores, &ct.App{Name: name, Meta: system})
	}
	for _, app := range stores {
		err := refuseDatastoreRunJob(app, req)
		if err == nil {
			t.Fatalf("%s allowed without host auth", app.Name)
		}
		if ve, ok := err.(ct.ValidationError); !ok || ve.Field != "app" || ve.Message != ct.DatastoreJobExecMessage {
			t.Fatalf("%s err = %v", app.Name, err)
		}
	}

	okReq := &http.Request{Header: http.Header{httphelper.HeaderFlynnHostAuth: []string{"host-secret"}}}
	if err := refuseDatastoreRunJob(store, okReq); err != nil {
		t.Fatalf("host auth: %v", err)
	}

	badReq := &http.Request{Header: http.Header{httphelper.HeaderFlynnHostAuth: []string{"nope"}}}
	if err := refuseDatastoreRunJob(store, badReq); err == nil {
		t.Fatal("wrong host auth must fail")
	}
}

func TestRefuseDatastoreRunJobFailsClosedWithoutHostKey(t *testing.T) {
	t.Setenv("FLYNN_HOST_AUTH_KEY", "")
	app := &ct.App{Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}}
	req := &http.Request{Header: http.Header{httphelper.HeaderFlynnHostAuth: []string{""}}}
	if err := refuseDatastoreRunJob(app, req); err == nil {
		t.Fatal("empty host key must not allow datastore RunJob")
	}
}

func TestSecretEqual(t *testing.T) {
	if !secretEqual("abc", "abc") {
		t.Fatal("equal")
	}
	if secretEqual("abc", "abd") || secretEqual("ab", "abc") || secretEqual("", "") || secretEqual("x", "") {
		t.Fatal("mismatched secrets must fail")
	}
}

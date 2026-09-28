package main

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"golang.org/x/net/context"
)

func TestCanManageDBRuntimes(t *testing.T) {
	host := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{ClusterKey: true})
	if !canManageDBRuntimes(host) {
		t.Fatal("flynn-host cluster key must be allowed")
	}
	admin := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{Scopes: []string{"cluster:admin"}})
	if !canManageDBRuntimes(admin) {
		t.Fatal("cluster admin must be allowed")
	}
	app := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{
		AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}},
	})
	if canManageDBRuntimes(app) {
		t.Fatal("app token must not create database runtimes")
	}
	if canManageDBRuntimes(context.Background()) {
		t.Fatal("missing token must not create database runtimes")
	}
}

func TestDBRuntimeCreateKeepsBuiltin(t *testing.T) {
	resetDBRuntimes()
	t.Cleanup(resetDBRuntimes)
	dbRuntimeMu.Lock()
	err := dbRuntimeCat.Create(dbruntime.Runtime{Engine: "redis", Name: "cache", CPU: 100, Memory: 128 << 20, Disk: 1 << 30})
	dbRuntimeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dbRuntimeCat.Find("redis", "small"); !ok {
		t.Fatal("builtin small missing")
	}
	if _, ok := dbRuntimeCat.Find("redis", "cache"); !ok {
		t.Fatal("custom runtime missing")
	}
}

func TestDBRuntimeUpdateAndRemove(t *testing.T) {
	resetDBRuntimes()
	t.Cleanup(resetDBRuntimes)
	cpu := int64(750)
	mem := int64(512 << 20)
	disk := int64(2 << 30)
	dbRuntimeMu.Lock()
	updated, err := dbRuntimeCat.Update("redis", "small", dbruntime.UpdateFields{CPU: &cpu, Memory: &mem, Disk: &disk})
	dbRuntimeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if updated.CPU != cpu || updated.Memory != mem || updated.Disk != disk {
		t.Fatalf("builtin update: %+v", updated)
	}
	dbRuntimeMu.Lock()
	if err := dbRuntimeCat.Create(dbruntime.Runtime{Engine: "redis", Name: "cache", CPU: 100, Memory: 128 << 20, Disk: 1 << 30}); err != nil {
		dbRuntimeMu.Unlock()
		t.Fatal(err)
	}
	if err := dbRuntimeCat.Remove("redis", "cache"); err != nil {
		dbRuntimeMu.Unlock()
		t.Fatal(err)
	}
	if err := dbRuntimeCat.Remove("redis", "small"); err == nil {
		dbRuntimeMu.Unlock()
		t.Fatal("builtin remove must fail")
	}
	dbRuntimeCat.AllowCustomSizes = true
	dbRuntimeMu.Unlock()
	if !dbRuntimeCat.AllowCustomSizes {
		t.Fatal("allow custom sizes not set")
	}
}

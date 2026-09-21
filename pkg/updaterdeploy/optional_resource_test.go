package updaterdeploy

import "testing"

func TestIsOptionalResourceApp(t *testing.T) {
	if IsOptionalResourceApp("clickhouse") {
		t.Fatal("clickhouse is a plugin, not an optional resource app")
	}
	if IsOptionalResourceApp("kafka") {
		t.Fatal("kafka is a plugin, not an optional resource app")
	}
	if IsOptionalResourceApp("redis") {
		t.Fatal("redis is not an optional resource app in this map")
	}
}

func TestEnsureApplianceControllerKey(t *testing.T) {
	if EnsureApplianceControllerKey(nil, "k") {
		t.Fatal("nil env")
	}
	env := map[string]string{}
	if EnsureApplianceControllerKey(env, "") {
		t.Fatal("empty key")
	}
	if !EnsureApplianceControllerKey(env, "secret") || env["CONTROLLER_KEY"] != "secret" {
		t.Fatalf("%v", env)
	}
	if EnsureApplianceControllerKey(env, "other") {
		t.Fatal("must not overwrite an existing key")
	}
	if env["CONTROLLER_KEY"] != "secret" {
		t.Fatalf("overwrote: %v", env)
	}
}

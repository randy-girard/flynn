package updaterdeploy

import "testing"

func TestIsOptionalResourceApp(t *testing.T) {
	if !IsOptionalResourceApp("clickhouse") {
		t.Fatal("expected clickhouse to be optional resource app")
	}
	if !IsOptionalResourceApp("kafka") {
		t.Fatal("expected kafka to be optional resource app")
	}
	if IsOptionalResourceApp("redis") {
		t.Fatal("redis is not an optional resource app in this map")
	}
}

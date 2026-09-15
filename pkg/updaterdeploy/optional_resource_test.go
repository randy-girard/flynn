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

package dbruntime

import (
	"errors"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/resource"
)

func limits(t *testing.T, r resource.Resources) (cpu, mem int64) {
	t.Helper()
	if r == nil {
		t.Fatal("no resources")
	}
	c, m := r[resource.TypeCPU], r[resource.TypeMemory]
	if c.Limit == nil || m.Limit == nil {
		t.Fatalf("limits missing %#v", r)
	}
	return *c.Limit, *m.Limit
}

func TestProcessResourcesForPublishedRuntime(t *testing.T) {
	cat := BuiltinCatalog()
	small, _ := cat.Find(EngineRedis, "small")
	for _, req := range [][2]int64{{0, 0}, {small.CPU, small.Memory}} {
		r, err := ProcessResourcesFor(&cat, EngineRedis, "small", req[0], req[1])
		if err != nil {
			t.Fatalf("%v: %v", req, err)
		}
		if cpu, mem := limits(t, r); cpu != small.CPU || mem != small.Memory {
			t.Fatalf("%v: cpu %d mem %d", req, cpu, mem)
		}
	}
}

func TestProcessResourcesForCustomNeedsAllowCustom(t *testing.T) {
	cat := BuiltinCatalog()
	_, err := ProcessResourcesFor(&cat, EngineRedis, "custom", 300, 300*mib)
	if !errors.Is(err, ErrCustomSizesDisabled) {
		t.Fatalf("custom without allow: %v", err)
	}
	// A tenant that bypasses the CLI with a published name and other numbers
	// is still asking for a custom size.
	_, err = ProcessResourcesFor(&cat, EngineRedis, "small", 4000, 0)
	if !errors.Is(err, ErrCustomSizesDisabled) {
		t.Fatalf("published name with other cpu: %v", err)
	}
	cat.AllowCustomSizes = true
	r, err := ProcessResourcesFor(&cat, EngineRedis, "custom", 300, 300*mib)
	if err != nil {
		t.Fatal(err)
	}
	if cpu, mem := limits(t, r); cpu != 300 || mem != 300*mib {
		t.Fatalf("custom cpu %d mem %d", cpu, mem)
	}
	if req := r[resource.TypeMemory].Request; req == nil || *req != 0 {
		t.Fatalf("custom must share capacity %#v", r)
	}
}

func TestProcessResourcesForFillsOmittedFieldFromDefault(t *testing.T) {
	cat := BuiltinCatalog()
	cat.AllowCustomSizes = true
	small, _ := cat.Find(EngineKafka, "small")
	r, err := ProcessResourcesFor(&cat, EngineKafka, "custom", 0, 768*mib)
	if err != nil {
		t.Fatal(err)
	}
	if cpu, mem := limits(t, r); cpu != small.CPU || mem != 768*mib {
		t.Fatalf("cpu %d mem %d", cpu, mem)
	}
	medium, _ := cat.Find(EngineKafka, "medium")
	r, err = ProcessResourcesFor(&cat, EngineKafka, "medium", 1500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cpu, mem := limits(t, r); cpu != 1500 || mem != medium.Memory {
		t.Fatalf("override on medium: cpu %d mem %d", cpu, mem)
	}
}

func TestProcessResourcesForRefusesBelowEngineMinimum(t *testing.T) {
	cat := BuiltinCatalog()
	cat.AllowCustomSizes = true
	for _, e := range Engines {
		min := MinMemory(e)
		if min <= 0 {
			t.Fatalf("%s has no memory minimum", e)
		}
		_, err := ProcessResourcesFor(&cat, e, "custom", 500, min-1)
		if err == nil || !strings.Contains(err.Error(), "at least") {
			t.Fatalf("%s below minimum: %v", e, err)
		}
		if _, err := ProcessResourcesFor(&cat, e, "custom", 500, min); err != nil {
			t.Fatalf("%s at minimum: %v", e, err)
		}
	}
	if _, err := ProcessResourcesFor(&cat, EngineRedis, "custom", MinCPU-1, 256*mib); err == nil {
		t.Fatal("cpu below minimum")
	}
	// Every builtin preset clears its engine's floor.
	for _, rt := range BuiltinCatalog().Runtimes {
		if rt.Memory < MinMemory(rt.Engine) || rt.CPU < MinCPU {
			t.Fatalf("builtin %s/%s is below the engine minimum", rt.Engine, rt.Name)
		}
	}
}

func TestProcessResourcesForUnpublishedOrNoCatalog(t *testing.T) {
	cat := BuiltinCatalog()
	r, err := ProcessResourcesFor(&cat, EngineRedis, "gone", 0, 0)
	if err != nil || r != nil {
		t.Fatalf("unpublished, nothing requested: %#v %v", r, err)
	}
	r, err = ProcessResourcesFor(nil, EngineRedis, "small", 0, 0)
	if err != nil || r != nil {
		t.Fatalf("no catalog: %#v %v", r, err)
	}
	if _, err := ProcessResourcesFor(nil, EngineRedis, "custom", 300, 300*mib); err == nil {
		t.Fatal("no catalog must refuse a custom size")
	}
	if _, err := ProcessResourcesFor(&cat, EngineRedis, "small", -1, 0); err == nil {
		t.Fatal("negative cpu")
	}
}

func TestProcessResourcesForKeepsDedicatedReserve(t *testing.T) {
	cat := BuiltinCatalog()
	if err := cat.Create(Runtime{Engine: EngineMySQL, Name: "node", CPU: 2000, Memory: 2 * gib, Disk: 10 * gib, Dedicated: true}); err != nil {
		t.Fatal(err)
	}
	r, err := ProcessResourcesFor(&cat, EngineMySQL, "node", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if req := r[resource.TypeMemory].Request; req == nil || *req != 2*gib {
		t.Fatalf("dedicated must reserve %#v", r)
	}
}

func TestMemoryLimitOf(t *testing.T) {
	if MemoryLimitOf(nil) != 0 {
		t.Fatal("nil")
	}
	r := make(resource.Resources)
	resource.ApplyNamedLimitsWithReserve(r, 300*mib, 300, false)
	if got := MemoryLimitOf(r); got != 300*mib {
		t.Fatalf("got %d", got)
	}
}

func TestEngineMemoryEnv(t *testing.T) {
	cases := []struct {
		engine string
		mem    int64
		want   map[string]string
	}{
		{EnginePostgres, 512 * mib, map[string]string{EnvSharedBuffers: "128MB", EnvEffectiveCacheSize: "384MB"}},
		{EngineMySQL, 512 * mib, map[string]string{EnvInnoDBBufferPoolSize: "268435456"}},
		{EngineRedis, 256 * mib, map[string]string{EnvRedisMaxMemory: "201326592"}},
		{EngineMongoDB, 1 * gib, map[string]string{EnvWiredTigerCacheGB: "0.25"}},
		{EngineMongoDB, 4 * gib, map[string]string{EnvWiredTigerCacheGB: "1.50"}},
		{EngineKafka, 1 * gib, map[string]string{EnvKafkaHeapOpts: "-Xms512m -Xmx512m"}},
		{EngineClickHouse, 2 * gib, map[string]string{EnvMaxServerMemoryUsage: "1717986918"}},
	}
	for _, c := range cases {
		env := EngineMemoryEnv(c.engine, c.mem)
		if env[EnvMemoryLimit] == "" {
			t.Fatalf("%s: no %s", c.engine, EnvMemoryLimit)
		}
		for k, v := range c.want {
			if env[k] != v {
				t.Fatalf("%s %d: %s=%q, want %q (%v)", c.engine, c.mem, k, env[k], v, env)
			}
		}
	}
	if env := EngineMemoryEnv(EngineRedis, 0); len(env) != 0 {
		t.Fatalf("no limit must set nothing: %v", env)
	}
	// Every derived setting leaves room under the limit at the engine floor.
	for _, e := range Engines {
		if len(EngineMemoryEnv(e, MinMemory(e))) < 2 {
			t.Fatalf("%s: no derived setting at the minimum", e)
		}
	}
}

func TestApplyEngineMemoryEnvReplacesStaleKeys(t *testing.T) {
	env := map[string]string{EnvRedisMaxMemory: "1", "OTHER": "x"}
	ApplyEngineMemoryEnv(env, EngineRedis, nil)
	if _, ok := env[EnvRedisMaxMemory]; ok || env["OTHER"] != "x" {
		t.Fatalf("no limit must clear derived keys: %v", env)
	}
	r := make(resource.Resources)
	resource.ApplyNamedLimitsWithReserve(r, 256*mib, 250, false)
	ApplyEngineMemoryEnv(env, EngineRedis, r)
	if env[EnvRedisMaxMemory] != "201326592" || env[EnvMemoryLimit] != "268435456" {
		t.Fatalf("env %v", env)
	}
}

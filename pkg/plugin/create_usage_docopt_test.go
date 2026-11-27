package plugin

import (
	"encoding/json"
	"testing"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/dbruntime"
)

// addons:create parses argv with CreateUsage. Options that take a value
// must parse as values, not as a flag plus a positional, or --runtime,
// --disk, --follow and the rest are silently dropped.
func TestCreateUsageParsesOptionValues(t *testing.T) {
	for _, typ := range []string{"redis", "postgres", "kafka"} {
		spec := OfficialAddon(typ)
		if spec == nil {
			t.Fatal(typ)
		}
		argv := []string{"addons:create", typ, "--unattached", "--owner", "acme", "--runtime", "large", "--cpu", "500", "--memory", "1GB", "--disk", "5G", "--as", "GOLD"}
		args, err := docopt.Parse(spec.CreateUsage(), argv, true, "", false)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		want := map[string]string{"--owner": "acme", "--runtime": "large", "--cpu": "500", "--memory": "1GB", "--disk": "5G", "--as": "GOLD"}
		for k, v := range want {
			if got := args.String[k]; got != v {
				t.Errorf("%s: %s = %q, want %q (args %#v)", typ, k, got, v, args.String)
			}
		}
		if !args.Bool["--unattached"] {
			t.Errorf("%s: --unattached not set", typ)
		}
	}
	pg := OfficialAddon("postgres")
	args, err := docopt.Parse(pg.CreateUsage(), []string{"addons:create", "postgres", "--follow", "pg-harbor-12345", "--replication", "logical", "--auto-failover"}, true, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if args.String["--follow"] != "pg-harbor-12345" || args.String["--replication"] != "logical" || !args.Bool["--auto-failover"] {
		t.Fatalf("follow args %#v %#v", args.String, args.Bool)
	}
}

// Omitted options stay empty.
func TestCreateUsageOmittedOptionsEmpty(t *testing.T) {
	args, err := docopt.Parse(OfficialAddon("redis").CreateUsage(), []string{"addons:create", "redis"}, true, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if args.String["--runtime"] != "" || args.String["--disk"] != "" || args.Bool["--unattached"] {
		t.Fatalf("args %#v %#v", args.String, args.Bool)
	}
}

// NEW-20: "addons:create redis --follow <primary> --auto-failover" must send
// both to the provider. Before CreateUsage declared value options, docopt
// dropped the --follow value and the plugin answered "auto-failover requires
// a follower (--follow)".
func TestCreateUsageRedisFollowAutoFailoverReachesProvider(t *testing.T) {
	spec := OfficialAddon("redis")
	args, err := docopt.Parse(spec.CreateUsage(), []string{"addons:create", "redis", "--follow", "redis-swamp-26938", "--auto-failover"}, true, "", false)
	if err != nil {
		t.Fatal(err)
	}
	cat := dbruntime.Catalog{}
	if err := cat.EnsureEngine(dbruntime.EngineRedis); err != nil {
		t.Fatal(err)
	}
	raw, err := spec.ProvisionConfig(CreateInput{Follow: args.String["--follow"], AutoFailover: args.Bool["--auto-failover"]}, cat)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["follow"] != "redis-swamp-26938" || body["auto_failover"] != true {
		t.Fatalf("provider config %s", raw)
	}
}

// NEW-10: "addons:create mysql --follow <primary>" reaches the provider as a
// follow (the KNOWN-02 docopt fix, which the redis test above covers).
// NEW-27: with no --runtime/--cpu/--memory/--disk the follow sends no
// runtime, so the plugin inherits the leader's.
func TestCreateUsageMysqlFollowReachesProviderWithoutRuntime(t *testing.T) {
	for _, typ := range []string{"mysql", "postgres", "redis"} {
		spec := OfficialAddon(typ)
		if spec == nil {
			t.Fatal(typ)
		}
		leader := typ + "-gorge-85967"
		args, err := docopt.Parse(spec.CreateUsage(), []string{"addons:create", typ, "--follow", leader}, true, "", false)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		cat := dbruntime.Catalog{}
		engine := typ
		if spec.Runtimes != nil && spec.Runtimes.Engine != "" {
			engine = spec.Runtimes.Engine
		}
		if err := cat.EnsureEngine(engine); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		raw, err := spec.ProvisionConfig(CreateInput{Follow: args.String["--follow"]}, cat)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["follow"] != leader {
			t.Fatalf("%s: provider config %s has no follow", typ, raw)
		}
		for _, k := range []string{"runtime", "tags", "dedicated", "cpu", "memory", "disk"} {
			if _, ok := body[k]; ok {
				t.Fatalf("%s: a follow with no size flags must not send %s (the plugin inherits the leader's): %s", typ, k, raw)
			}
		}
		// An explicit --runtime still wins.
		raw, err = spec.ProvisionConfig(CreateInput{Follow: leader, Runtime: spec.Runtimes.Default}, cat)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if err := json.Unmarshal(raw, &body); err != nil || body["runtime"] != spec.Runtimes.Default {
			t.Fatalf("%s: explicit runtime dropped: %s", typ, raw)
		}
	}
}

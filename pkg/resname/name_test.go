package resname

import (
	"regexp"
	"testing"
)

func TestNameShapeAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^pg-[a-z]+-[a-z]{6}$`)
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		name := Name("pg", func(n string) bool { return seen[n] })
		if !re.MatchString(name) {
			t.Fatalf("name %q", name)
		}
		if seen[name] {
			t.Fatalf("duplicate %q", name)
		}
		seen[name] = true
	}
	blocked := Name("redis", func(string) bool { return true })
	if len(blocked) < len("redis-amber-abcdefgh")-2 {
		t.Fatalf("exhausted names should grow the suffix: %q", blocked)
	}
}

func TestMergeAttachmentKeepsTheFirstURL(t *testing.T) {
	firstIn := map[string]string{
		"FLYNN_POSTGRES": "pg-delta-pclpez",
		"DATABASE_URL":   "postgres://first",
		"POSTGRES_URL":   "postgres://first",
	}
	first := MergeAttachment(nil, firstIn, "")
	if first["DATABASE_URL"] != "postgres://first" || first["POSTGRES_URL"] != "postgres://first" || first["PG_DELTA_DATABASE_URL"] != "postgres://first" {
		t.Fatalf("first postgres: %#v", first)
	}
	second := MergeAttachment(first, map[string]string{
		"FLYNN_POSTGRES": "pg-harbor-kxmnpq",
		"DATABASE_URL":   "postgres://second",
		"POSTGRES_URL":   "postgres://second",
	}, "")
	if second["DATABASE_URL"] != "" || second["POSTGRES_URL"] != "" || second["FLYNN_POSTGRES"] != "" {
		t.Fatalf("second postgres must not replace the first attachment: %#v", second)
	}
	if second["PG_HARBOR_DATABASE_URL"] != "postgres://second" {
		t.Fatalf("named url: %#v", second)
	}

	redis := MergeAttachment(map[string]string{"DATABASE_URL": "postgres://app"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-xefywh",
		"REDIS_URL":   "rediss://cache",
	}, "")
	if redis["REDIS_URL"] != "rediss://cache" || redis["REDIS_EMBER_DATABASE_URL"] != "rediss://cache" {
		t.Fatalf("first redis keeps REDIS_URL and its own name: %#v", redis)
	}
	again := MergeAttachment(map[string]string{"REDIS_URL": "rediss://first"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-xefywh",
		"REDIS_URL":   "rediss://second",
	}, "")
	if again["REDIS_URL"] != "" || again["REDIS_EMBER_DATABASE_URL"] != "rediss://second" {
		t.Fatalf("second redis: %#v", again)
	}

	ch := MergeAttachment(map[string]string{"CLICKHOUSE_URL": "clickhouses://old"}, map[string]string{
		"FLYNN_CLICKHOUSE": "clickhouse-meadow-wawpuf",
		"CLICKHOUSE_URL":   "clickhouses://new",
	}, "")
	if ch["CLICKHOUSE_MEADOW_DATABASE_URL"] != "clickhouses://new" || ch["CLICKHOUSE_URL"] != "" {
		t.Fatalf("clickhouse: %#v", ch)
	}
}

func TestResourceEnvKeepsIdentityOnSecond(t *testing.T) {
	first := ResourceEnv(nil, map[string]string{
		"FLYNN_POSTGRES": "pg-delta-pclpez",
		"DATABASE_URL":   "postgres://first",
		"POSTGRES_URL":   "postgres://first",
		"POSTGRES_ROLE":  "primary",
		"PGDATABASE":     "db_first",
	}, "")
	if first["FLYNN_POSTGRES"] != "pg-delta-pclpez" || first["POSTGRES_ROLE"] != "primary" {
		t.Fatalf("first resource: %#v", first)
	}
	second := ResourceEnv(first, map[string]string{
		"FLYNN_POSTGRES":  "pg-harbor-kxmnpq",
		"DATABASE_URL":    "postgres://second",
		"POSTGRES_URL":    "postgres://second",
		"POSTGRES_ROLE":   "follower",
		"POSTGRES_LEADER": "pg-delta-pclpez",
		"PGDATABASE":      "db_first",
	}, "")
	if second["DATABASE_URL"] != "" || second["POSTGRES_URL"] != "" {
		t.Fatalf("second must not steal the app URL: %#v", second)
	}
	if second["FLYNN_POSTGRES"] != "pg-harbor-kxmnpq" || second["POSTGRES_ROLE"] != "follower" || second["POSTGRES_LEADER"] != "pg-delta-pclpez" {
		t.Fatalf("second resource must keep its instance identity: %#v", second)
	}
	if second["PG_HARBOR_DATABASE_URL"] != "postgres://second" {
		t.Fatalf("named url: %#v", second)
	}
}

func TestMergeAttachmentHonorsAs(t *testing.T) {
	got := MergeAttachment(nil, map[string]string{
		"FLYNN_POSTGRES": "pg-harbor-kxmnpq",
		"DATABASE_URL":   "postgres://db",
	}, "analytics")
	if got["ANALYTICS_URL"] != "postgres://db" || got["PG_HARBOR_DATABASE_URL"] != "postgres://db" || got["DATABASE_URL"] != "postgres://db" {
		t.Fatalf("as + named + conventional: %#v", got)
	}
	taken := MergeAttachment(map[string]string{"DATABASE_URL": "postgres://old"}, map[string]string{
		"FLYNN_POSTGRES": "pg-harbor-kxmnpq",
		"DATABASE_URL":   "postgres://db",
	}, "analytics")
	if taken["DATABASE_URL"] != "" || taken["ANALYTICS_URL"] != "postgres://db" || taken["PG_HARBOR_DATABASE_URL"] != "postgres://db" {
		t.Fatalf("as must not replace DATABASE_URL: %#v", taken)
	}
}

func TestExtraDatabaseURLUsesSuffixWhenTheWordIsTaken(t *testing.T) {
	key := ExtraDatabaseURL("pg-harbor-kxmnpq", func(k string) bool { return k == "PG_HARBOR_DATABASE_URL" })
	if key != "PG_HARBOR_KXMNPQ_DATABASE_URL" {
		t.Fatalf("key %q", key)
	}
}

func TestIdentity(t *testing.T) {
	if name, key := Identity(nil); name != "" || key != "" {
		t.Fatalf("nil: %q %q", name, key)
	}
	name, key := Identity(map[string]string{"FLYNN_POSTGRES": "pg-harbor-kxmnpq"})
	if name != "pg-harbor-kxmnpq" || key != "DATABASE_URL" {
		t.Fatalf("postgres: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"FLYNN_MYSQL": "mysql-fjord-abcxyz"})
	if name != "mysql-fjord-abcxyz" || key != "DATABASE_URL" {
		t.Fatalf("mysql: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"FLYNN_REDIS": "redis-ember-xefywh"})
	if name != "redis-ember-xefywh" || key != "REDIS_URL" {
		t.Fatalf("redis: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"FLYNN_KAFKA": "kafka-grove-aaaaaa"})
	if name != "kafka-grove-aaaaaa" || key != "KAFKA_URL" {
		t.Fatalf("kafka: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"FLYNN_CLICKHOUSE": "clickhouse-meadow-wawpuf"})
	if name != "clickhouse-meadow-wawpuf" || key != "CLICKHOUSE_URL" {
		t.Fatalf("clickhouse: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"FLYNN_MONGO": "mongo-basin-bbbbbb"})
	if name != "mongo-basin-bbbbbb" || key != "DATABASE_URL" {
		t.Fatalf("mongo: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"FLYNN_MONGODB": "mongodb-cedar-cccccc"})
	if name != "mongodb-cedar-cccccc" || key != "DATABASE_URL" {
		t.Fatalf("mongodb: %q %q", name, key)
	}
	name, key = Identity(map[string]string{"DATABASE_URL": "postgres://x"})
	if name != "" || key != "" {
		t.Fatalf("no flynn key: %q %q", name, key)
	}
}

func TestCanonical(t *testing.T) {
	if got := Canonical("pg", "harbor-kxmnpq"); got != "pg-harbor-kxmnpq" {
		t.Fatalf("bare name: %q", got)
	}
	if got := Canonical("pg", "pg-harbor-kxmnpq"); got != "pg-harbor-kxmnpq" {
		t.Fatalf("full name: %q", got)
	}
	if got := Canonical("redis", "redis-6f0e1c2a-uuid"); got != "redis-6f0e1c2a-uuid" {
		t.Fatalf("existing app: %q", got)
	}
	if got := Canonical("pg", ""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

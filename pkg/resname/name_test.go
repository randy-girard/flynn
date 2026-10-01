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
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://first",
		"POSTGRES_URL":   "postgres://first",
		"PGHOST":         "leader.postgresql-concave-48291.discoverd",
		"PGUSER":         "app",
		"PGPASSWORD":     "secret",
	}
	first := MergeAttachment(nil, firstIn, "")
	firstKey := postgresColorKey(first)
	if firstKey == "" || first[firstKey] != "postgres://first" {
		t.Fatalf("first postgres: %#v", first)
	}
	if first["DATABASE_URL"] != "" {
		t.Fatalf("postgres must not set DATABASE_URL: %#v", first)
	}
	if _, ok := first["POSTGRES_URL"]; ok {
		t.Fatalf("app must not get POSTGRES_URL: %#v", first)
	}
	if _, ok := first["PGHOST"]; ok || first["FLYNN_POSTGRES"] != "" || first["PGUSER"] != "" {
		t.Fatalf("app must not get PG/POSTGRES/FLYNN keys: %#v", first)
	}
	second := MergeAttachment(first, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://second",
		"POSTGRES_URL":   "postgres://second",
		"PGHOST":         "leader.postgresql-concave-48291.discoverd",
	}, "")
	secondKey := postgresColorKey(second)
	if second["DATABASE_URL"] != "" || second["POSTGRES_URL"] != "" || second["FLYNN_POSTGRES"] != "" || second["PGHOST"] != "" {
		t.Fatalf("second postgres must not replace the first attachment: %#v", second)
	}
	if secondKey == "" || secondKey == firstKey || second[secondKey] != "postgres://second" {
		t.Fatalf("named url: first %s second %#v", firstKey, second)
	}

	redis := MergeAttachment(map[string]string{"DATABASE_URL": "postgres://app"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-xefywh",
		"REDIS_URL":   "rediss://cache",
		"REDIS_HOST":  "leader.redis-ember-xefywh.discoverd",
	}, "")
	if redis["REDIS_URL"] != "rediss://cache" || redis["REDIS_EMBER_XEFYWH_DATABASE_URL"] != "rediss://cache" {
		t.Fatalf("first redis keeps REDIS_URL and its own name: %#v", redis)
	}
	if redis["REDIS_HOST"] != "" {
		t.Fatalf("redis must not set REDIS_HOST on the app: %#v", redis)
	}
	again := MergeAttachment(map[string]string{"REDIS_URL": "rediss://first"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-xefywh",
		"REDIS_URL":   "rediss://second",
	}, "")
	if again["REDIS_URL"] != "" || again["REDIS_EMBER_XEFYWH_DATABASE_URL"] != "rediss://second" {
		t.Fatalf("second redis: %#v", again)
	}

	ch := MergeAttachment(map[string]string{"CLICKHOUSE_URL": "clickhouses://old"}, map[string]string{
		"FLYNN_CLICKHOUSE": "clickhouse-meadow-wawpuf",
		"CLICKHOUSE_URL":   "clickhouses://new",
	}, "")
	if ch["CLICKHOUSE_MEADOW_WAWPUF_DATABASE_URL"] != "clickhouses://new" || ch["CLICKHOUSE_URL"] != "" {
		t.Fatalf("clickhouse: %#v", ch)
	}
}

func TestResourceEnvKeepsIdentityOnSecond(t *testing.T) {
	first := ResourceEnv(nil, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://first",
		"POSTGRES_URL":   "postgres://first",
		"POSTGRES_ROLE":  "primary",
		"PGDATABASE":     "db_first",
	}, "")
	if first["FLYNN_POSTGRES"] != "postgresql-concave-48291" || first["POSTGRES_ROLE"] != "primary" {
		t.Fatalf("first resource: %#v", first)
	}
	if first["DATABASE_URL"] != "" || first["POSTGRES_URL"] != "" {
		t.Fatalf("first resource URLs: %#v", first)
	}
	if postgresColorKey(first) == "" {
		t.Fatalf("first resource missing color URL: %#v", first)
	}
	if _, ok := first["PGDATABASE"]; ok || first["PGHOST"] != "" || first["PGUSER"] != "" {
		t.Fatalf("resource must not keep split PG keys: %#v", first)
	}
	second := ResourceEnv(first, map[string]string{
		"FLYNN_POSTGRES":  "postgresql-fjord-99112",
		"DATABASE_URL":    "postgres://second",
		"POSTGRES_URL":    "postgres://second",
		"POSTGRES_ROLE":   "follower",
		"POSTGRES_LEADER": "postgresql-concave-48291",
		"PGDATABASE":      "db_first",
	}, "")
	if second["DATABASE_URL"] != "" {
		t.Fatalf("second must not steal the app DATABASE_URL: %#v", second)
	}
	if second["POSTGRES_URL"] != "" {
		t.Fatalf("resource must not store POSTGRES_URL: %#v", second)
	}
	if second["FLYNN_POSTGRES"] != "postgresql-fjord-99112" || second["POSTGRES_ROLE"] != "follower" || second["POSTGRES_LEADER"] != "postgresql-concave-48291" {
		t.Fatalf("second resource must keep its instance identity: %#v", second)
	}
	if postgresColorKey(second) == "" || postgresColorKey(second) == postgresColorKey(first) {
		t.Fatalf("named url: %#v vs %#v", first, second)
	}
	if _, ok := second["PGDATABASE"]; ok {
		t.Fatalf("second must not keep PGDATABASE: %#v", second)
	}
}

func TestMergeAttachmentHonorsAs(t *testing.T) {
	got := MergeAttachment(nil, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "analytics")
	if got["ANALYTICS_URL"] != "postgres://db" || got["DATABASE_URL"] != "" || postgresColorKey(got) != "" {
		t.Fatalf("--as analytics is ANALYTICS_URL only: %#v", got)
	}
	if AsURLKey("ANALYTICS_URL") != "ANALYTICS_URL" || AsURLKey("analytics") != "ANALYTICS_URL" {
		t.Fatalf("AsURLKey: %q %q", AsURLKey("ANALYTICS_URL"), AsURLKey("analytics"))
	}
	color := MergeAttachment(nil, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "amber")
	if color["FLYNN_POSTGRESQL_AMBER_URL"] != "postgres://db" || color["AMBER_URL"] != "" || color["DATABASE_URL"] != "" {
		t.Fatalf("--as amber: %#v", color)
	}
	full := MergeAttachment(map[string]string{"FLYNN_POSTGRESQL_AMBER_URL": "postgres://old"}, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "FLYNN_POSTGRESQL_BLUE")
	if full["FLYNN_POSTGRESQL_BLUE_URL"] != "postgres://db" || full["FLYNN_POSTGRESQL_AMBER_URL"] != "" {
		t.Fatalf("--as FLYNN_POSTGRESQL_BLUE: %#v", full)
	}
}

func TestExtraDatabaseURLUsesTheFullResourceName(t *testing.T) {
	key := ExtraDatabaseURL("pg-harbor-kxmnpq", nil)
	if key != "PG_HARBOR_KXMNPQ_DATABASE_URL" {
		t.Fatalf("key %q", key)
	}
	key = ExtraDatabaseURL("pg-harbor-kxmnpq", func(k string) bool { return k == "PG_HARBOR_KXMNPQ_DATABASE_URL" })
	if key != "PG_HARBOR_KXMNPQ_X_DATABASE_URL" {
		t.Fatalf("taken key %q", key)
	}
}

func TestUnsetAttachmentRemovesGenericAndScopedKeys(t *testing.T) {
	release := map[string]string{
		"DATABASE_URL":                 "postgres://first",
		"PGHOST":                       "leader.pg-delta-pclpez.discoverd",
		"PG_DELTA_PCLPEZ_DATABASE_URL": "postgres://first",
		"PG_DELTA_PCLPEZ_PGHOST":       "leader.pg-delta-pclpez.discoverd",
		"KEEP":                         "yes",
	}
	resource := map[string]string{
		"FLYNN_POSTGRES":               "pg-delta-pclpez",
		"DATABASE_URL":                 "postgres://first",
		"POSTGRES_URL":                 "postgres://first",
		"PGHOST":                       "leader.pg-delta-pclpez.discoverd",
		"PG_DELTA_PCLPEZ_DATABASE_URL": "postgres://first",
		"PG_DELTA_PCLPEZ_PGHOST":       "leader.pg-delta-pclpez.discoverd",
	}
	got := UnsetAttachment(release, resource)
	if got["DATABASE_URL"] != nil || got["PGHOST"] != nil || got["PG_DELTA_PCLPEZ_DATABASE_URL"] != nil || got["PG_DELTA_PCLPEZ_PGHOST"] != nil {
		t.Fatalf("must unset attachment keys: %#v", got)
	}
	if _, ok := got["KEEP"]; ok {
		t.Fatalf("must leave unrelated env: %#v", got)
	}
}

func TestUnsetAttachmentURLOnlyApp(t *testing.T) {
	release := map[string]string{
		"DATABASE_URL":                 "postgres://first",
		"PG_DELTA_PCLPEZ_DATABASE_URL": "postgres://first",
		"KEEP":                         "yes",
	}
	resource := map[string]string{
		"FLYNN_POSTGRES":               "pg-delta-pclpez",
		"DATABASE_URL":                 "postgres://first",
		"POSTGRES_URL":                 "postgres://first",
		"PGHOST":                       "leader.pg-delta-pclpez.discoverd",
		"PG_DELTA_PCLPEZ_DATABASE_URL": "postgres://first",
	}
	got := UnsetAttachment(release, resource)
	if got["DATABASE_URL"] != nil || got["PG_DELTA_PCLPEZ_DATABASE_URL"] != nil {
		t.Fatalf("must unset URL keys: %#v", got)
	}
	if _, ok := got["PGHOST"]; ok {
		t.Fatalf("app had no PGHOST: %#v", got)
	}
	if _, ok := got["KEEP"]; ok {
		t.Fatalf("must leave unrelated env: %#v", got)
	}
}

func TestLockedKeysMatchReleaseValues(t *testing.T) {
	release := map[string]string{
		"FLYNN_POSTGRESQL_AMBER_URL": "postgres://first",
		"PGHOST":                     "leader.postgresql-concave-48291.discoverd",
		"NOTES":                      "x",
	}
	locked := LockedKeys(release, map[string]string{
		"FLYNN_POSTGRES":             "postgresql-concave-48291",
		"POSTGRES_URL":               "postgres://first",
		"PGHOST":                     "leader.postgresql-concave-48291.discoverd",
		"FLYNN_POSTGRESQL_AMBER_URL": "postgres://first",
	}, map[string]string{
		"FLYNN_POSTGRES": "postgresql-fjord-99112",
		"PGHOST":         "leader.postgresql-fjord-99112.discoverd",
	})
	if locked["FLYNN_POSTGRESQL_AMBER_URL"] != "postgres://first" {
		t.Fatalf("attachment URLs: %#v", locked)
	}
	if _, ok := locked["PGHOST"]; ok {
		t.Fatalf("PGHOST must not be locked on the app: %#v", locked)
	}
	if _, ok := locked["NOTES"]; ok {
		t.Fatalf("unrelated: %#v", locked)
	}
	peer := map[string]string{"FLYNN_POSTGRESQL_BLUE_URL": "postgres://first"}
	peerLocked := LockedKeys(peer, map[string]string{
		"FLYNN_POSTGRES":             "postgresql-concave-48291",
		"FLYNN_POSTGRESQL_AMBER_URL": "postgres://first",
	})
	if peerLocked["FLYNN_POSTGRESQL_BLUE_URL"] != "postgres://first" {
		t.Fatalf("peer color URL must lock by connection string: %#v", peerLocked)
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
	if got := Canonical("pg", "postgresql-concave-48291"); got != "postgresql-concave-48291" {
		t.Fatalf("postgresql instance: %q", got)
	}
	if got := Canonical("redis", "redis-6f0e1c2a-uuid"); got != "redis-6f0e1c2a-uuid" {
		t.Fatalf("existing app: %q", got)
	}
	if got := Canonical("pg", ""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestIsolatedService(t *testing.T) {
	for _, name := range []string{"postgresql-concave-48291", "postgresql-shop-482913", "postgresql-resource-demo-100001", "pg-ridge-ffpade", "pg-harbor-kxmnpq", "mysql-orchid-aaaaaa", "redis-juniper-abcdef"} {
		if !IsolatedService(name) {
			t.Fatalf("%q should be an isolated datastore", name)
		}
	}
	for _, name := range []string{"postgres", "postgres-plugin", "postgresql", "pg-api", "shop-web", "leader.pg-ridge-ffpade", "pg--ffpade", "pg-ridge-ffpade1", "postgresql-shop-abc123", "postgresql-concave-4829"} {
		if IsolatedService(name) {
			t.Fatalf("%q must not look like an isolated datastore", name)
		}
	}
}

func TestPostgresInstanceName(t *testing.T) {
	re := regexp.MustCompile(`^postgresql-[a-z]+-[0-9]{5}$`)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		name := PostgresInstanceName(func(n string) bool { return seen[n] })
		if !re.MatchString(name) {
			t.Fatalf("name %q", name)
		}
		seen[name] = true
	}
}

func postgresColorKey(env map[string]string) string {
	for k := range env {
		if PostgresColorURLKey(k) {
			return k
		}
	}
	return ""
}

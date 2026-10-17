package resname

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
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
	first := MergeAttachment(nil, firstIn, "", true)
	if first["DATABASE_URL"] != "postgres://first" {
		t.Fatalf("new provision must set DATABASE_URL: %#v", first)
	}
	if postgresColorKey(first) == "" {
		t.Fatalf("provision must also set a color URL: %#v", first)
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
	}, "", true)
	secondKey := postgresColorKey(second)
	if second["DATABASE_URL"] != "" || second["POSTGRES_URL"] != "" || second["FLYNN_POSTGRES"] != "" || second["PGHOST"] != "" {
		t.Fatalf("second postgres must not replace the first attachment: %#v", second)
	}
	if secondKey == "" || second[secondKey] != "postgres://second" {
		t.Fatalf("named url: first DATABASE_URL second %#v", second)
	}
	same := MergeAttachment(first, firstIn, "", true)
	if len(same) != 0 {
		t.Fatalf("reattach of the same URL must not add another key: %#v", same)
	}
	attachIn := map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://first",
	}
	if k := postgresColorKey(first); k != "" {
		attachIn[k] = first[k]
	}
	peer := MergeAttachment(nil, attachIn, "", false)
	if peer["DATABASE_URL"] != "" {
		t.Fatalf("attach of an existing resource must not set DATABASE_URL: %#v", peer)
	}
	if postgresColorKey(peer) == "" || peer[postgresColorKey(peer)] != "postgres://first" {
		t.Fatalf("attach must set the color URL: %#v", peer)
	}

	redis := MergeAttachment(map[string]string{"DATABASE_URL": "postgres://app"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-48291",
		"REDIS_URL":   "rediss://cache",
		"REDIS_HOST":  "leader.redis-ember-48291.discoverd",
	}, "", true)
	if redis["REDIS_URL"] != "rediss://cache" {
		t.Fatalf("new redis provision must set REDIS_URL: %#v", redis)
	}
	if redis["REDIS_EMBER_48291_DATABASE_URL"] != "" || redis["FLYNN_REDIS"] != "" || redis["REDIS_HOST"] != "" {
		t.Fatalf("redis must not set instance-named URL or identity on the app: %#v", redis)
	}
	if redisColorKey(redis) == "" {
		t.Fatalf("redis provision must also set a color URL: %#v", redis)
	}
	again := MergeAttachment(map[string]string{"REDIS_URL": "rediss://first"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-48291",
		"REDIS_URL":   "rediss://second",
	}, "", true)
	if again["REDIS_URL"] != "" {
		t.Fatalf("second redis must not steal REDIS_URL: %#v", again)
	}
	if redisColorKey(again) == "" || again[redisColorKey(again)] != "rediss://second" {
		t.Fatalf("second redis: %#v", again)
	}
	redisPeer := MergeAttachment(nil, map[string]string{
		"FLYNN_REDIS":          "redis-ember-48291",
		"REDIS_URL":            "rediss://cache",
		"FLYNN_REDIS_TEAL_URL": "rediss://cache",
	}, "", false)
	if redisPeer["REDIS_URL"] != "" {
		t.Fatalf("attach of existing redis must not set REDIS_URL: %#v", redisPeer)
	}
	if redisPeer["FLYNN_REDIS_TEAL_URL"] != "rediss://cache" {
		t.Fatalf("attach must reuse incoming redis color: %#v", redisPeer)
	}

	ch := MergeAttachment(map[string]string{"CLICKHOUSE_URL": "clickhouses://old"}, map[string]string{
		"FLYNN_CLICKHOUSE": "clickhouse-meadow-wawpuf",
		"CLICKHOUSE_URL":   "clickhouses://new",
	}, "", false)
	if clickhouseColorKey(ch) == "" || ch[clickhouseColorKey(ch)] != "clickhouses://new" || ch["CLICKHOUSE_URL"] != "" {
		t.Fatalf("clickhouse: %#v", ch)
	}
	if ch["CLICKHOUSE_MEADOW_WAWPUF_DATABASE_URL"] != "" {
		t.Fatalf("clickhouse must not use the instance-named URL: %#v", ch)
	}
}

func TestMergeAttachmentColorSchemeForAllEngines(t *testing.T) {
	type engine struct {
		identity string
		conv     string
		prefix   string
	}
	engines := []engine{
		{"FLYNN_MYSQL", "DATABASE_URL", "FLYNN_MYSQL_"},
		{"FLYNN_MONGODB", "DATABASE_URL", "FLYNN_MONGODB_"},
		{"FLYNN_CLICKHOUSE", "CLICKHOUSE_URL", "FLYNN_CLICKHOUSE_"},
		{"FLYNN_KAFKA", "KAFKA_URL", "FLYNN_KAFKA_"},
	}
	for _, e := range engines {
		in := map[string]string{e.identity: "x", e.conv: "scheme://db"}
		prov := MergeAttachment(nil, in, "", true)
		if prov[e.conv] != "scheme://db" {
			t.Fatalf("%s new provision must set %s: %#v", e.identity, e.conv, prov)
		}
		if colorKeyForPrefix(prov, e.prefix) == "" {
			t.Fatalf("%s provision must also set a color URL: %#v", e.identity, prov)
		}
		if prov[e.identity] != "" {
			t.Fatalf("%s identity must stay on the resource, not the app: %#v", e.identity, prov)
		}
		// Second provision must not steal the conventional URL, but still gets a color.
		second := MergeAttachment(prov, map[string]string{e.identity: "y", e.conv: "scheme://db2"}, "", true)
		if second[e.conv] != "" {
			t.Fatalf("%s second provision must not steal %s: %#v", e.identity, e.conv, second)
		}
		if colorKeyForPrefix(second, e.prefix) == "" || second[colorKeyForPrefix(second, e.prefix)] != "scheme://db2" {
			t.Fatalf("%s second provision must pick a color: %#v", e.identity, second)
		}
		// Attach of an existing resource never sets the conventional URL.
		attach := MergeAttachment(nil, in, "", false)
		if attach[e.conv] != "" {
			t.Fatalf("%s attach must not set %s: %#v", e.identity, e.conv, attach)
		}
		if colorKeyForPrefix(attach, e.prefix) == "" {
			t.Fatalf("%s attach must set a color URL: %#v", e.identity, attach)
		}
		// --as NAME is NAME_URL; --as COLOR is the color URL.
		asName := MergeAttachment(nil, in, "analytics", true)
		if asName["ANALYTICS_URL"] != "scheme://db" || colorKeyForPrefix(asName, e.prefix) != "" {
			t.Fatalf("%s --as analytics: %#v", e.identity, asName)
		}
		asColor := MergeAttachment(nil, in, "amber", true)
		if asColor[e.prefix+"AMBER_URL"] != "scheme://db" || asColor["AMBER_URL"] != "" {
			t.Fatalf("%s --as amber: %#v", e.identity, asColor)
		}
		// Incoming color is reused so record and release share one key.
		reuse := MergeAttachment(nil, map[string]string{
			e.identity:            "z",
			e.conv:                "scheme://db",
			e.prefix + "TEAL_URL": "scheme://db",
		}, "", false)
		if reuse[e.prefix+"TEAL_URL"] != "scheme://db" || colorKeyForPrefix(reuse, e.prefix) != e.prefix+"TEAL_URL" {
			t.Fatalf("%s must reuse incoming color: %#v", e.identity, reuse)
		}
	}
}

func TestResourceEnvKeepsIdentityOnSecond(t *testing.T) {
	first := ResourceEnv(nil, map[string]string{
		"FLYNN_POSTGRES":      "postgresql-concave-48291",
		"DATABASE_URL":        "postgres://first",
		"POSTGRES_URL":        "postgres://first",
		"FLYNN_POSTGRES_ROLE": "primary",
		"PGDATABASE":          "db_first",
	}, "")
	if first["FLYNN_POSTGRES"] != "postgresql-concave-48291" || first["FLYNN_POSTGRES_ROLE"] != "primary" {
		t.Fatalf("first resource: %#v", first)
	}
	if first["DATABASE_URL"] != "postgres://first" {
		t.Fatalf("first resource must keep DATABASE_URL: %#v", first)
	}
	if first["POSTGRES_URL"] != "" {
		t.Fatalf("first resource URLs: %#v", first)
	}
	if postgresColorKey(first) == "" {
		t.Fatalf("first resource must also keep a color URL: %#v", first)
	}
	if _, ok := first["PGDATABASE"]; ok || first["PGHOST"] != "" || first["PGUSER"] != "" {
		t.Fatalf("resource must not keep split PG keys: %#v", first)
	}
	second := ResourceEnv(first, map[string]string{
		"FLYNN_POSTGRES":        "postgresql-fjord-99112",
		"DATABASE_URL":          "postgres://second",
		"POSTGRES_URL":          "postgres://second",
		"FLYNN_POSTGRES_ROLE":   "follower",
		"FLYNN_POSTGRES_LEADER": "postgresql-concave-48291",
		"PGDATABASE":            "db_first",
	}, "")
	if second["DATABASE_URL"] != "" {
		t.Fatalf("second must not steal the app DATABASE_URL: %#v", second)
	}
	if second["POSTGRES_URL"] != "" {
		t.Fatalf("resource must not store POSTGRES_URL: %#v", second)
	}
	if second["FLYNN_POSTGRES"] != "postgresql-fjord-99112" || second["FLYNN_POSTGRES_ROLE"] != "follower" || second["FLYNN_POSTGRES_LEADER"] != "postgresql-concave-48291" {
		t.Fatalf("second resource must keep its instance identity: %#v", second)
	}
	if postgresColorKey(second) == "" {
		t.Fatalf("named url: %#v vs %#v", first, second)
	}
	if _, ok := second["PGDATABASE"]; ok {
		t.Fatalf("second must not keep PGDATABASE: %#v", second)
	}
}

func TestResourceEnvDropsExtraPostgresColors(t *testing.T) {
	got := ResourceEnv(nil, map[string]string{
		"FLYNN_POSTGRES":               "postgresql-concave-48291",
		"DATABASE_URL":                 "postgres://first",
		"FLYNN_POSTGRESQL_CRIMSON_URL": "postgres://first",
		"FLYNN_POSTGRESQL_SAGE_URL":    "postgres://first",
		"FLYNN_POSTGRES_ROLE":          "primary",
	}, "")
	if got["DATABASE_URL"] != "postgres://first" {
		t.Fatalf("DATABASE_URL: %#v", got)
	}
	if postgresColorKey(got) != "FLYNN_POSTGRESQL_CRIMSON_URL" {
		t.Fatalf("resource must keep one reused color URL: %#v", got)
	}
	if got["FLYNN_POSTGRESQL_SAGE_URL"] != "" {
		t.Fatalf("resource must drop extra incoming colors: %#v", got)
	}
	n := 0
	for k := range got {
		if strings.HasSuffix(k, "_URL") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("DATABASE_URL plus one color: %#v", got)
	}
}

func TestMergeAttachmentCopiesKafkaClientEnv(t *testing.T) {
	in := map[string]string{
		"FLYNN_KAFKA":                   "kafka-meadow-29864",
		"KAFKA_URL":                     "kafka+ssl://leader.kafka-meadow-29864.discoverd:9092",
		"FLYNN_KAFKA_TLS_ENABLED":       "true",
		"FLYNN_KAFKA_TRUSTED_CERT":      "CA",
		"FLYNN_KAFKA_CLIENT_CERT":       "CERT",
		"FLYNN_KAFKA_CLIENT_CERT_KEY":   "KEY",
		"FLYNN_KAFKA_SASL_MECHANISM":    "SCRAM-SHA-256",
		"FLYNN_KAFKA_SASL_USERNAME":     "u-tenant",
		"FLYNN_KAFKA_SASL_PASSWORD":     "pw",
		"FLYNN_KAFKA_TOPIC_PREFIX":      "flynn.abc.",
		"FLYNN_KAFKA_BOOTSTRAP_SERVERS": "leader.kafka-meadow-29864.discoverd:9092",
		"KAFKA_TLS_SERVER_KEY":          "broker-secret",
	}
	got := MergeAttachment(nil, in, "", true)
	for _, k := range []string{"FLYNN_KAFKA_TLS_ENABLED", "FLYNN_KAFKA_TRUSTED_CERT", "FLYNN_KAFKA_CLIENT_CERT", "FLYNN_KAFKA_CLIENT_CERT_KEY", "FLYNN_KAFKA_SASL_USERNAME", "FLYNN_KAFKA_SASL_PASSWORD", "FLYNN_KAFKA_TOPIC_PREFIX", "FLYNN_KAFKA_BOOTSTRAP_SERVERS"} {
		if got[k] != in[k] {
			t.Fatalf("%s: %#v", k, got)
		}
	}
	if got["KAFKA_TLS_SERVER_KEY"] != "" || got["FLYNN_KAFKA"] != "" {
		t.Fatalf("broker key and identity must stay off the app: %#v", got)
	}
	withCA := MergeAttachment(nil, map[string]string{
		"FLYNN_MONGODB":              "mongodb-pebble-50574",
		"DATABASE_URL":               "mongodb://leader.mongodb-pebble-50574.discoverd/db?tls=true",
		"FLYNN_MONGODB_TRUSTED_CERT": "CA",
	}, "", true)
	if withCA["FLYNN_MONGODB_TRUSTED_CERT"] != "CA" {
		t.Fatalf("mongo ca: %#v", withCA)
	}
	if got["KAFKA_URL"] == "" {
		t.Fatal(got)
	}
}

func TestMergeAttachmentHonorsAs(t *testing.T) {
	got := MergeAttachment(nil, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "analytics", true)
	if got["ANALYTICS_URL"] != "postgres://db" || got["DATABASE_URL"] != "postgres://db" || postgresColorKey(got) != "" {
		t.Fatalf("provision --as analytics is ANALYTICS_URL plus DATABASE_URL: %#v", got)
	}
	attachAs := MergeAttachment(nil, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "analytics", false)
	if attachAs["ANALYTICS_URL"] != "postgres://db" || attachAs["DATABASE_URL"] != "" || postgresColorKey(attachAs) != "" {
		t.Fatalf("attach --as analytics is ANALYTICS_URL only: %#v", attachAs)
	}
	if AsURLKey("ANALYTICS_URL") != "ANALYTICS_URL" || AsURLKey("analytics") != "ANALYTICS_URL" {
		t.Fatalf("AsURLKey: %q %q", AsURLKey("ANALYTICS_URL"), AsURLKey("analytics"))
	}
	color := MergeAttachment(nil, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "amber", true)
	if color["FLYNN_POSTGRESQL_AMBER_URL"] != "postgres://db" || color["AMBER_URL"] != "" || color["DATABASE_URL"] != "postgres://db" {
		t.Fatalf("provision --as amber: %#v", color)
	}
	full := MergeAttachment(map[string]string{"FLYNN_POSTGRESQL_AMBER_URL": "postgres://old"}, map[string]string{
		"FLYNN_POSTGRES": "postgresql-concave-48291",
		"DATABASE_URL":   "postgres://db",
	}, "FLYNN_POSTGRESQL_BLUE", true)
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

func TestLockedKeysAttachmentVars(t *testing.T) {
	release := map[string]string{
		"FLYNN_POSTGRES_ROLE":      "primary",
		"POSTGRES_ROLE":            "primary",
		"FLYNN_KAFKA_TRUSTED_CERT": "ca",
		"NOTES":                    "x",
		"PGHOST":                   "leader.example",
	}
	locked := LockedKeys(release, map[string]string{
		"FLYNN_POSTGRES":      "postgresql-concave-48291",
		"FLYNN_POSTGRES_ROLE": "primary",
		"POSTGRES_ROLE":       "primary",
		"PGHOST":              "leader.example",
	})
	for _, key := range []string{"FLYNN_POSTGRES_ROLE", "POSTGRES_ROLE", "FLYNN_KAFKA_TRUSTED_CERT"} {
		if locked[key] == "" {
			t.Fatalf("missing %s: %#v", key, locked)
		}
	}
	if _, ok := locked["NOTES"]; ok {
		t.Fatalf("unrelated: %#v", locked)
	}
	if _, ok := locked["PGHOST"]; ok {
		t.Fatalf("PGHOST must stay writable: %#v", locked)
	}

	only := LockedKeys(map[string]string{"FLYNN_REDIS_ROLE": "primary", "FOO": "bar"})
	if only["FLYNN_REDIS_ROLE"] != "primary" {
		t.Fatalf("prefixed key stays locked: %#v", only)
	}
	if _, ok := only["FOO"]; ok {
		t.Fatalf("unrelated: %#v", only)
	}

	legacy := LockedKeys(map[string]string{"POSTGRES_ROLE": "primary"})
	if _, ok := legacy["POSTGRES_ROLE"]; ok {
		t.Fatalf("legacy name is writable once no resource owns it: %#v", legacy)
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
	for _, name := range []string{"postgresql-concave-48291", "postgresql-chaparral-48291", "postgresql-woodland-48291", "postgresql-shop-482913", "postgresql-resource-demo-100001", "pg-ridge-ffpade", "pg-harbor-kxmnpq", "mysql-orchid-aaaaaa", "mysql-yarrow-aaaaaa", "mysql-inlet-48291", "mysql-woodland-48291", "redis-juniper-abcdef", "redis-harbor-48291", "redis-woodland-48291", "redis-shop-482913", "mongodb-harbor-48291", "clickhouse-reef-48291", "kafka-inlet-48291"} {
		if !IsolatedService(name) {
			t.Fatalf("%q should be an isolated datastore", name)
		}
	}
	for _, name := range []string{"postgres", "postgres-plugin", "postgresql", "pg-api", "shop-web", "leader.pg-ridge-ffpade", "pg--ffpade", "pg-ridge-ffpade1", "postgresql-shop-abc123", "postgresql-concave-4829", "redis", "redis-plugin", "redis-harbor-4829", "mysql", "mysql-plugin", "mysql-inlet-4829", "mongodb-harbor-4829", "kafka-plugin", "clickhouse-api"} {
		if IsolatedService(name) {
			t.Fatalf("%q must not look like an isolated datastore", name)
		}
	}
}

func TestWordAndColorListsUniqueSorted(t *testing.T) {
	seen := map[string]bool{}
	for i, w := range words {
		if w == "" || w != strings.ToLower(w) {
			t.Fatalf("word %q", w)
		}
		for _, c := range w {
			if c < 'a' || c > 'z' {
				t.Fatalf("word %q must be a-z", w)
			}
		}
		if i > 0 && w <= words[i-1] {
			t.Fatalf("words must be sorted unique: %q after %q", w, words[i-1])
		}
		if seen[w] {
			t.Fatalf("duplicate word %q", w)
		}
		seen[w] = true
	}
	if len(words) < 50 {
		t.Fatalf("want a larger word list, got %d", len(words))
	}
	seenColor := map[string]bool{}
	for i, c := range attachmentColors {
		if c == "" || c != strings.ToUpper(c) {
			t.Fatalf("color %q", c)
		}
		for _, r := range c {
			if r < 'A' || r > 'Z' {
				t.Fatalf("color %q must be A-Z", c)
			}
		}
		if i > 0 && c <= attachmentColors[i-1] {
			t.Fatalf("colors must be sorted unique: %q after %q", c, attachmentColors[i-1])
		}
		if seenColor[c] {
			t.Fatalf("duplicate color %q", c)
		}
		seenColor[c] = true
	}
	if len(attachmentColors) < 16 {
		t.Fatalf("want the core color list, got %d", len(attachmentColors))
	}
}

func TestPostgresAttachmentURLKeyRecognizesExtendedColor(t *testing.T) {
	if got := PostgresAttachmentURLKey("amber", nil); got != "FLYNN_POSTGRESQL_AMBER_URL" {
		t.Fatalf("got %q", got)
	}
	if got := PostgresAttachmentURLKey("chartreuse", nil); got != "CHARTREUSE_URL" {
		t.Fatalf("got %q", got)
	}
	if !PostgresColorURLKey("FLYNN_POSTGRESQL_CHARTREUSE_URL") {
		t.Fatal("an existing extended color URL stays attached")
	}
	if got := RedisAttachmentURLKey("blue", nil); got != "FLYNN_REDIS_BLUE_URL" {
		t.Fatalf("got %q", got)
	}
	if !IsolatedService("postgresql-alder-48291") || !IsolatedService("redis-glacier-48291") {
		t.Fatal("new word stems must still be isolated instance names")
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

func TestRedisInstanceName(t *testing.T) {
	re := regexp.MustCompile(`^redis-[a-z]+-[0-9]{5}$`)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		name := RedisInstanceName(func(n string) bool { return seen[n] })
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

func redisColorKey(env map[string]string) string {
	for k := range env {
		if RedisColorURLKey(k) {
			return k
		}
	}
	return ""
}

func clickhouseColorKey(env map[string]string) string {
	for k := range env {
		if ClickHouseColorURLKey(k) {
			return k
		}
	}
	return ""
}

func colorKeyForPrefix(env map[string]string, prefix string) string {
	for k := range env {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, "_URL") &&
			strings.TrimSuffix(strings.TrimPrefix(k, prefix), "_URL") != "" {
			return k
		}
	}
	return ""
}

// attachmentColors is duplicated in the engine plugins and in
// flynn/pkg/resname. The digest freezes the list so a copy cannot drift
// silently; when a color is added or removed, update the digest in all seven
// places at once.
const attachmentColorsDigest = "66cf0e3f123af773c2080403799064d0957d8df68ec028e915de613f87da63a4"

func TestAttachmentColorsFrozen(t *testing.T) {
	sum := sha256.Sum256([]byte(strings.Join(attachmentColors, ",")))
	if got := hex.EncodeToString(sum[:]); got != attachmentColorsDigest {
		t.Fatalf("attachment palette changed (%d colors): digest %s != %s; update this digest in flynn/pkg/resname and every engine plugin", len(attachmentColors), got, attachmentColorsDigest)
	}
}

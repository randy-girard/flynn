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
	first := map[string]string{
		"FLYNN_POSTGRES": "pg-delta-pclpez",
		"DATABASE_URL":   "postgres://first",
		"POSTGRES_URL":   "postgres://first",
	}
	second := map[string]string{
		"FLYNN_POSTGRES": "pg-harbor-kxmnpq",
		"DATABASE_URL":   "postgres://second",
		"POSTGRES_URL":   "postgres://second",
	}
	got := MergeAttachment(first, second)
	if got["DATABASE_URL"] != "" || got["POSTGRES_URL"] != "" || got["FLYNN_POSTGRES"] != "" {
		t.Fatalf("second postgres must not replace the first attachment: %#v", got)
	}
	if got["PG_HARBOR_DATABASE_URL"] != "postgres://second" {
		t.Fatalf("named url: %#v", got)
	}

	redis := MergeAttachment(map[string]string{"DATABASE_URL": "postgres://app"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-xefywh",
		"REDIS_URL":   "rediss://cache",
	})
	if redis["REDIS_URL"] != "rediss://cache" || redis["REDIS_EMBER_DATABASE_URL"] != "" {
		t.Fatalf("first redis keeps REDIS_URL: %#v", redis)
	}
	again := MergeAttachment(map[string]string{"REDIS_URL": "rediss://first"}, map[string]string{
		"FLYNN_REDIS": "redis-ember-xefywh",
		"REDIS_URL":   "rediss://second",
	})
	if again["REDIS_URL"] != "" || again["REDIS_EMBER_DATABASE_URL"] != "rediss://second" {
		t.Fatalf("second redis: %#v", again)
	}

	ch := MergeAttachment(map[string]string{"CLICKHOUSE_URL": "clickhouses://old"}, map[string]string{
		"FLYNN_CLICKHOUSE": "clickhouse-meadow-wawpuf",
		"CLICKHOUSE_URL":   "clickhouses://new",
	})
	if ch["CLICKHOUSE_MEADOW_DATABASE_URL"] != "clickhouses://new" || ch["CLICKHOUSE_URL"] != "" {
		t.Fatalf("clickhouse: %#v", ch)
	}
}

func TestExtraDatabaseURLUsesSuffixWhenTheWordIsTaken(t *testing.T) {
	key := ExtraDatabaseURL("pg-harbor-kxmnpq", func(k string) bool { return k == "PG_HARBOR_DATABASE_URL" })
	if key != "PG_HARBOR_KXMNPQ_DATABASE_URL" {
		t.Fatalf("key %q", key)
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

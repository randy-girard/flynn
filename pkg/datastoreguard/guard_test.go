package datastoreguard

import (
	"errors"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestReferencesEveryDataPlugin(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		app  string
	}{
		{"postgres url", map[string]string{"DATABASE_URL": "postgres://u:p@leader.postgresql-canyon-28550.discoverd:5432/db"}, "postgresql-canyon-28550"},
		{"postgres service", map[string]string{"FLYNN_POSTGRES": "postgresql-canyon-28550"}, "postgresql-canyon-28550"},
		{"postgres leader", map[string]string{"FLYNN_POSTGRES_LEADER": "postgresql-canyon-28550"}, "postgresql-canyon-28550"},
		{"redis url", map[string]string{"REDIS_URL": "redis://h:x@leader.redis-basin-45047.discoverd:6379"}, "redis-basin-45047"},
		{"mysql url", map[string]string{"MYSQL_URL": "mysql://u:p@leader.mysql-timber-28982.discoverd:3306/db"}, "mysql-timber-28982"},
		{"mongodb url", map[string]string{"MONGODB_URL": "mongodb://u:p@leader.mongodb-tide-44875.discoverd:27017/db"}, "mongodb-tide-44875"},
		{"kafka url", map[string]string{"FLYNN_KAFKA_AMBER_URL": "kafka://leader.kafka-ember-81658.discoverd:9092"}, "kafka-ember-81658"},
		{"clickhouse url", map[string]string{"FLYNN_CLICKHOUSE_URL": "https://u:p@leader.clickhouse-grove-83617.discoverd:9440"}, "clickhouse-grove-83617"},
	}
	for _, tc := range cases {
		if !References(tc.env, tc.app) {
			t.Errorf("%s: env must count as a reference", tc.name)
		}
	}
}

func TestReferencesIgnoresShorterNames(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://u:p@leader.postgresql-canyon-28550.discoverd:5432/db"}
	if References(env, "postgres") || References(env, "postgresql-canyon") {
		t.Fatal("a shorter name must not match a different datastore host")
	}
}

func TestReferencingAppSkipsTheDatastoreItself(t *testing.T) {
	src := &memReleases{apps: []*ct.App{
		{ID: "db", Name: "postgresql-canyon-28550"},
		{ID: "tenant", Name: "app-one"},
	}, rel: map[string]*ct.Release{
		"db":     {Env: map[string]string{"FLYNN_POSTGRES": "postgresql-canyon-28550"}},
		"tenant": {Env: map[string]string{"DATABASE_URL": "postgres://u:p@leader.postgresql-canyon-28550.discoverd:5432/db"}},
	}}
	who, err := ReferencingApp(src, "postgresql-canyon-28550")
	if err != nil {
		t.Fatal(err)
	}
	if who != "app-one" {
		t.Fatalf("referencer %q", who)
	}
	src.rel["tenant"] = &ct.Release{Env: map[string]string{}}
	who, err = ReferencingApp(src, "postgresql-canyon-28550")
	if err != nil || who != "" {
		t.Fatalf("unattached datastore who=%q err=%v", who, err)
	}
}

func TestRefuseIfReferencedFailsClosed(t *testing.T) {
	err := RefuseIfReferenced(nil, "postgresql-canyon-28550")
	if err == nil {
		t.Fatal("missing source must refuse the delete")
	}
	src := &memReleases{err: errors.New("controller unavailable")}
	err = RefuseIfReferenced(src, "redis-basin-45047")
	if err == nil {
		t.Fatal("a failed lookup must refuse the delete")
	}
}

type memReleases struct {
	apps []*ct.App
	rel  map[string]*ct.Release
	err  error
}

func (m *memReleases) AppList() ([]*ct.App, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.apps, nil
}

func (m *memReleases) GetAppRelease(id string) (*ct.Release, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rel[id], nil
}

// NEW-31: after a keep-service failover the promoted primary claims the old
// primary's discoverd name (FLYNN_REDIS_SERVICE_ALIAS) and the old primary
// follows the promoted one (FLYNN_REDIS_LEADER). Counting the alias as a data
// reference refused both deletes, so addons:destroy leaked both apps.
func failedOverRedisPair() *memReleases {
	return &memReleases{apps: []*ct.App{
		{ID: "old", Name: "redis-lagoon-04896"},
		{ID: "new", Name: "redis-estuary-39405"},
	}, rel: map[string]*ct.Release{
		"old": {Env: map[string]string{
			"FLYNN_REDIS":        "redis-lagoon-04896",
			"FLYNN_REDIS_ROLE":   "follower",
			"FLYNN_REDIS_LEADER": "redis-estuary-39405",
			"REDIS_MASTER_HOST":  "leader.redis-lagoon-04896.discoverd",
		}},
		"new": {Env: map[string]string{
			"FLYNN_REDIS":               "redis-estuary-39405",
			"FLYNN_REDIS_ROLE":          "primary",
			"FLYNN_REDIS_SERVICE_ALIAS": "redis-lagoon-04896",
		}},
	}}
}

func TestRefuseIfReferencedAllowsOldPrimaryAfterFailover(t *testing.T) {
	src := failedOverRedisPair()
	if err := RefuseIfReferenced(src, "redis-lagoon-04896"); err != nil {
		t.Fatalf("the promoted primary's service alias must not block deleting the old primary: %v", err)
	}
}

func TestRefuseIfReferencedKeepsPromotedPrimaryWhileOldPrimaryFollows(t *testing.T) {
	src := failedOverRedisPair()
	if err := RefuseIfReferenced(src, "redis-estuary-39405"); err == nil {
		t.Fatal("the old primary still replicates from the promoted one; its delete must wait")
	}
	src.apps = src.apps[1:]
	delete(src.rel, "old")
	if err := RefuseIfReferenced(src, "redis-estuary-39405"); err != nil {
		t.Fatalf("with the old primary gone the promoted primary must be deletable: %v", err)
	}
}

func TestReferencesIgnoresServiceAliasForEveryFailoverPlugin(t *testing.T) {
	for _, key := range []string{"FLYNN_REDIS_SERVICE_ALIAS", "FLYNN_POSTGRES_SERVICE_ALIAS", "FLYNN_MYSQL_SERVICE_ALIAS"} {
		if References(map[string]string{key: "db-old-1"}, "db-old-1") {
			t.Errorf("%s claims a discoverd name; it does not read that app's data", key)
		}
	}
	env := map[string]string{"FLYNN_REDIS_SERVICE_ALIAS": "db-old-1", "REDIS_URL": "redis://:x@leader.db-old-1.discoverd:6379"}
	if !References(env, "db-old-1") {
		t.Fatal("an alias must not hide a real reference in another key")
	}
}

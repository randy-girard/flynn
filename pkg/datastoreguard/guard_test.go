package datastoreguard

import (
	"errors"
	"strings"
	"testing"
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
	src := &memRefs{refs: []AppRef{
		{ID: "db", Name: "postgresql-canyon-28550", Env: map[string]string{"FLYNN_POSTGRES": "postgresql-canyon-28550"}},
		{ID: "tenant", Name: "app-one", Env: map[string]string{"DATABASE_URL": "postgres://u:p@leader.postgresql-canyon-28550.discoverd:5432/db"}},
	}}
	who, err := ReferencingApp(src, "postgresql-canyon-28550")
	if err != nil {
		t.Fatal(err)
	}
	if who != "app-one" {
		t.Fatalf("referencer %q", who)
	}
	src.refs[1].Env = map[string]string{}
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
	src := &memRefs{err: errors.New("controller unavailable")}
	err = RefuseIfReferenced(src, "redis-basin-45047")
	if err == nil {
		t.Fatal("a failed lookup must refuse the delete")
	}
}

type memRefs struct {
	refs  []AppRef
	err   error
	calls int
}

func (m *memRefs) AppRefs() ([]AppRef, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.refs, nil
}

// NEW-31: after a keep-service failover the promoted primary claims the old
// primary's discoverd name (FLYNN_REDIS_SERVICE_ALIAS) and the old primary
// follows the promoted one (FLYNN_REDIS_LEADER). Counting the alias as a data
// reference refused both deletes, so addons:destroy leaked both apps.
func failedOverRedisPair() *memRefs {
	return &memRefs{refs: []AppRef{
		{ID: "old", Name: "redis-lagoon-04896", Env: map[string]string{
			"FLYNN_REDIS":        "redis-lagoon-04896",
			"FLYNN_REDIS_ROLE":   "follower",
			"FLYNN_REDIS_LEADER": "redis-estuary-39405",
			"REDIS_MASTER_HOST":  "leader.redis-lagoon-04896.discoverd",
		}},
		{ID: "new", Name: "redis-estuary-39405", Env: map[string]string{
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
	src.refs = src.refs[1:]
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

// CORE-37: release env is tenant input. Tenant B putting the name of tenant
// A's datastore into its own env must not hold back A's delete. Only an app
// of the same owner account, or one attached to a resource that points at
// the datastore (a provider-written row), counts.
func TestReferencingAppIgnoresAppsWithoutAttachment(t *testing.T) {
	const ds = "redis-harbor-48291"
	attached := map[string]string{"FLYNN_REDIS": ds, "REDIS_URL": "redis://:pw@leader." + ds + ".discoverd:6379"}
	datastore := AppRef{ID: "ds", Name: ds, Env: map[string]string{"FLYNN_REDIS": ds}}
	tenantB := AppRef{ID: "b", Name: "b-web", OwnerAccount: "user:b", Env: map[string]string{"X": ds}}
	tenantBURL := AppRef{ID: "b2", Name: "b-worker", OwnerAccount: "user:b", Env: map[string]string{"REDIS_URL": attached["REDIS_URL"]}}
	src := &memRefs{refs: []AppRef{datastore, tenantB, tenantBURL}}

	who, err := ReferencingApp(src, ds)
	if err != nil {
		t.Fatal(err)
	}
	if who != "" {
		t.Fatalf("another account's env named the datastore; %q must not block its delete", who)
	}
	if err := RefuseIfReferenced(src, ds); err != nil {
		t.Fatalf("delete refused by an unattached app: %v", err)
	}

	// The consumer the provider attached still blocks the delete.
	consumer := AppRef{ID: "a", Name: "a-web", OwnerAccount: "user:a", Env: map[string]string{"REDIS_URL": attached["REDIS_URL"]}, ResourceEnvs: []map[string]string{attached}}
	src.refs = append(src.refs, consumer)
	if who, err := ReferencingApp(src, ds); err != nil || who != "a-web" {
		t.Fatalf("attached consumer must block the delete, who=%q err=%v", who, err)
	}
	err = RefuseIfReferenced(src, ds)
	if err == nil || strings.Contains(err.Error(), "b-") {
		t.Fatalf("refusal must name the attached app only: %v", err)
	}

	// An attachment to a different datastore does not vouch for this one.
	other := map[string]string{"FLYNN_REDIS": "redis-cove-11111"}
	src.refs = []AppRef{datastore, {ID: "b", Name: "b-web", OwnerAccount: "user:b", Env: map[string]string{"X": ds}, ResourceEnvs: []map[string]string{other}}}
	if who, err := ReferencingApp(src, ds); err != nil || who != "" {
		t.Fatalf("an attachment to another datastore must not count, who=%q err=%v", who, err)
	}
}

// Apps of the datastore's own owner account still count without an
// attachment: a follower datastore app the plugin created (no owner, like the
// primary) and a tenant's own app pointing at its own app.
func TestReferencingAppCountsSameOwnerAccount(t *testing.T) {
	src := failedOverRedisPair()
	if err := RefuseIfReferenced(src, "redis-estuary-39405"); err == nil {
		t.Fatal("an ownerless follower must still hold back its ownerless primary")
	}
	tenant := &memRefs{refs: []AppRef{
		{ID: "api", Name: "a-api", OwnerAccount: "org:a"},
		{ID: "web", Name: "a-web", OwnerAccount: "org:a", Env: map[string]string{"API_APP": "a-api"}},
		{ID: "x", Name: "b-web", OwnerAccount: "user:b", Env: map[string]string{"API_APP": "a-api"}},
	}}
	if who, err := ReferencingApp(tenant, "a-api"); err != nil || who != "a-web" {
		t.Fatalf("same-account app must count, who=%q err=%v", who, err)
	}
	tenant.refs = append(tenant.refs[:1], tenant.refs[2])
	if who, err := ReferencingApp(tenant, "a-api"); err != nil || who != "" {
		t.Fatalf("another account's app must not count, who=%q err=%v", who, err)
	}
}

// The guard reads the app list, releases and attachments once per delete,
// not one release per app.
func TestReferencingAppReadsSourceOnce(t *testing.T) {
	src := failedOverRedisPair()
	for i := 0; i < 50; i++ {
		src.refs = append(src.refs, AppRef{ID: "t" + string(rune('a'+i%26)), Name: "tenant-app", OwnerAccount: "user:t"})
	}
	if _, err := ReferencingApp(src, "redis-lagoon-04896"); err != nil {
		t.Fatal(err)
	}
	if src.calls != 1 {
		t.Fatalf("source read %d times", src.calls)
	}
}

// A datastore missing from the snapshot has an unknown owner; every env
// reference counts so the delete fails closed.
func TestReferencingAppUnknownTargetCountsEveryReference(t *testing.T) {
	src := &memRefs{refs: []AppRef{{ID: "b", Name: "b-web", OwnerAccount: "user:b", Env: map[string]string{"X": "redis-gone-1"}}}}
	if who, err := ReferencingApp(src, "redis-gone-1"); err != nil || who != "b-web" {
		t.Fatalf("who=%q err=%v", who, err)
	}
}

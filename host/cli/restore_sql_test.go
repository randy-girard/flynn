package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
)

// BUILD-16: the SQL that `flynn-host bootstrap --from-backup` appends to the
// restored dump quotes every value it writes, so a quote, semicolon, comment,
// backslash, dollar quote or newline in an image manifest, artifact meta,
// URI, app or process name is stored as written and cannot end the
// statement.

// nasty holds the characters that broke or escaped the old '%s' SQL.
const nasty = "it's; DROP TABLE canary; -- \\ \"q\" $$ $x$ }{, é 日本\n'); DELETE FROM apps; --"

func restoreSQLConn(t *testing.T) *pgx.Conn {
	t.Helper()
	if err := pgtestutils.SetupPostgres("hostcli_restore_sql"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgtestutils.ConnConfigForDatabase("hostcli_restore_sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, stmt := range []string{
		`CREATE TABLE canary (x int)`,
		`CREATE TABLE apps (app_id serial, name text, release_id int, deleted_at timestamptz)`,
		`CREATE TABLE releases (release_id int, env jsonb, processes jsonb)`,
		`CREATE TABLE formations (release_id int, processes jsonb)`,
		`CREATE TABLE artifacts (artifact_id uuid, uri text, type text, manifest jsonb, hashes jsonb, size int, layer_url_template text, meta jsonb)`,
		`CREATE TABLE release_artifacts (release_id int, artifact_id uuid)`,
		`CREATE TABLE events (object_type text, created_at timestamptz)`,
		`CREATE TABLE job_cache (job_id int, created_at timestamptz)`,
		`CREATE TABLE volumes (volume_id int, created_at timestamptz)`,
		`CREATE TABLE job_volumes (job_id int, volume_id int)`,
		restoreSQLFunctions,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %s", stmt, err)
		}
	}
	return conn
}

func execRestoreSQL(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(sql); err != nil {
		t.Fatalf("restore SQL failed: %s\n%s", err, sql)
	}
	var tables int
	if err := conn.QueryRow(`SELECT count(*) FROM pg_tables WHERE tablename = 'canary'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("the canary table is gone (%d, %v): the SQL was injected\n%s", tables, err, sql)
	}
	var apps int
	if err := conn.QueryRow(`SELECT count(*) FROM apps`).Scan(&apps); err != nil || apps != 2 {
		t.Fatalf("apps = %d, %v: the SQL was injected\n%s", apps, err, sql)
	}
}

func TestRestoreArtifactSQLQuotesManifest(t *testing.T) {
	conn := restoreSQLConn(t)
	for _, stmt := range []string{
		`INSERT INTO apps (name, release_id) VALUES (` + sqlLiteralForTest(nasty) + `, 1), ('redis', 2)`,
		`INSERT INTO releases VALUES (1, '{}', '{}'), (2, '{"REDIS_IMAGE_URI": "old"}', '{}')`,
		`INSERT INTO artifacts (artifact_id, uri) VALUES ('11111111-1111-1111-1111-111111111111', 'old'), ('22222222-2222-2222-2222-222222222222', 'old')`,
		`INSERT INTO release_artifacts VALUES (1, '11111111-1111-1111-1111-111111111111')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	manifest := json.RawMessage(`{"entrypoints":{"_default":{"args":["sh","-c","echo '` + "it's; -- $$" + `'"]}},"note":"` + strings.ReplaceAll(strings.ReplaceAll(nasty, `\`, `\\`), "\n", `\n`) + `"}`)
	manifest = json.RawMessage(strings.ReplaceAll(string(manifest), `"q"`, `\"q\"`))
	if !json.Valid(manifest) {
		t.Fatalf("bad fixture manifest %s", manifest)
	}
	art := &ct.Artifact{
		URI:              "http://blobstore.discoverd/x/" + nasty,
		RawManifest:      manifest,
		Hashes:           map[string]string{"sha512_256": nasty},
		Size:             42,
		LayerURLTemplate: "http://blobstore.discoverd/layers/{id}.squashfs?" + nasty,
		Meta:             map[string]string{"k'; --": nasty},
	}
	redis := &ct.Artifact{URI: "redis-" + nasty, RawManifest: manifest, Hashes: art.Hashes, Size: art.Size, LayerURLTemplate: art.LayerURLTemplate, Meta: art.Meta}
	execRestoreSQL(t, conn, restoreArtifactSQL(nasty, art)+restoreRedisArtifactSQL(redis))

	var uri, tmpl string
	var gotManifest, hashes, meta []byte
	var size int64
	if err := conn.QueryRow(`SELECT uri, manifest::text, hashes::text, size, layer_url_template, meta::text FROM artifacts WHERE artifact_id = '11111111-1111-1111-1111-111111111111'`).Scan(&uri, &gotManifest, &hashes, &size, &tmpl, &meta); err != nil {
		t.Fatal(err)
	}
	if uri != art.URI || tmpl != art.LayerURLTemplate || size != 42 {
		t.Fatalf("uri=%q tmpl=%q size=%d", uri, tmpl, size)
	}
	var m1, m2 interface{}
	json.Unmarshal(manifest, &m1)
	json.Unmarshal(gotManifest, &m2)
	if mustJSONString(m1) != mustJSONString(m2) {
		t.Fatalf("manifest = %s, want %s", gotManifest, manifest)
	}
	var gotHashes, gotMeta map[string]string
	json.Unmarshal(hashes, &gotHashes)
	json.Unmarshal(meta, &gotMeta)
	if gotHashes["sha512_256"] != nasty || gotMeta["k'; --"] != nasty {
		t.Fatalf("hashes=%s meta=%s", hashes, meta)
	}
	var redisURI, redisEnv string
	if err := conn.QueryRow(`SELECT a.uri, r.env->>'REDIS_IMAGE_ID' FROM artifacts a, releases r WHERE a.artifact_id = '22222222-2222-2222-2222-222222222222' AND r.release_id = 2`).Scan(&redisURI, &redisEnv); err != nil {
		t.Fatal(err)
	}
	if redisURI != redis.URI || redisEnv != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("redis uri=%q env=%q", redisURI, redisEnv)
	}
}

func TestRestoreReleaseSQLQuotesNames(t *testing.T) {
	conn := restoreSQLConn(t)
	procsJSON := mustJSONString(map[string]interface{}{nasty: map[string]interface{}{"env": map[string]string{"SINGLETON": "false", "KEEP": "1"}}})
	for _, stmt := range []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT INTO apps (name, release_id) VALUES ($1, 1), ('discoverd', 2)`, []interface{}{nasty}},
		{`INSERT INTO releases VALUES (1, '{"SINGLETON": "false"}', $1::jsonb), (2, '{"A": "b"}', '{}')`, []interface{}{procsJSON}},
		{`INSERT INTO formations VALUES (1, '{}')`, nil},
		{`INSERT INTO events VALUES ('job', '2000-01-01'), ('job', '2100-01-01')`, nil},
		{`INSERT INTO job_cache VALUES (1, '2000-01-01')`, nil},
		{`INSERT INTO volumes VALUES (1, '2000-01-01')`, nil},
		{`INSERT INTO job_volumes VALUES (1, 1)`, nil},
	} {
		if _, err := conn.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %s", stmt.sql, err)
		}
	}
	sql := dropProcessSingletonSQL(nasty, nasty) +
		setSingletonSQL("true", []string{"postgres", nasty}) +
		formationScaleSQL(nasty, nasty, 1) +
		releaseEnvKeySQL("discoverd", "DISCOVERD_PEERS", nasty) +
		releaseEnvJSONSetSQL(nasty, "TELEMETRY_CLUSTER_ID", nasty) +
		removeStaleRecordsSQL("2050-01-01T00:00:00Z")
	execRestoreSQL(t, conn, sql)

	var env, procs, formation, peers string
	if err := conn.QueryRow(`SELECT env::text, processes::text FROM releases WHERE release_id = 1`).Scan(&env, &procs); err != nil {
		t.Fatal(err)
	}
	var gotEnv map[string]string
	json.Unmarshal([]byte(env), &gotEnv)
	if gotEnv["SINGLETON"] != "true" || gotEnv["TELEMETRY_CLUSTER_ID"] != nasty {
		t.Fatalf("env = %s", env)
	}
	var gotProcs map[string]map[string]map[string]string
	json.Unmarshal([]byte(procs), &gotProcs)
	if e := gotProcs[nasty]["env"]; e == nil || e["KEEP"] != "1" || e["SINGLETON"] != "" {
		t.Fatalf("processes = %s, want SINGLETON dropped from the process env", procs)
	}
	if err := conn.QueryRow(`SELECT processes::text FROM formations WHERE release_id = 1`).Scan(&formation); err != nil {
		t.Fatal(err)
	}
	var gotFormation map[string]int
	json.Unmarshal([]byte(formation), &gotFormation)
	if gotFormation[nasty] != 1 || len(gotFormation) != 1 {
		t.Fatalf("formation = %s", formation)
	}
	if err := conn.QueryRow(`SELECT env->>'DISCOVERD_PEERS' FROM releases WHERE release_id = 2`).Scan(&peers); err != nil || peers != nasty {
		t.Fatalf("peers = %q, %v", peers, err)
	}
	var events, jobs, vols, jobVols int
	if err := conn.QueryRow(`SELECT (SELECT count(*) FROM events), (SELECT count(*) FROM job_cache), (SELECT count(*) FROM volumes), (SELECT count(*) FROM job_volumes)`).Scan(&events, &jobs, &vols, &jobVols); err != nil {
		t.Fatal(err)
	}
	if events != 1 || jobs != 0 || vols != 0 || jobVols != 0 {
		t.Fatalf("stale records left: events=%d jobs=%d volumes=%d job_volumes=%d", events, jobs, vols, jobVols)
	}
}

func TestSQLLiteral(t *testing.T) {
	for in, want := range map[string]string{
		"":         "''",
		"plain":    "'plain'",
		"it's":     "'it''s'",
		`a\b`:      `'a\b'`,
		"''":       "''''''",
		"x;--y\nz": "'x;--y\nz'",
	} {
		if got := sqlLiteral(in); got != want {
			t.Errorf("sqlLiteral(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestPsqlConnectLine(t *testing.T) {
	if got, err := psqlConnectLine("controller"); err != nil || got != "\\connect 'controller'\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := psqlConnectLine("a'b"); err != nil || got != "\\connect 'a''b'\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "a\nb", "a\rb", "a\x00b"} {
		if _, err := psqlConnectLine(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// No restore SQL is built by putting a value between quotes with Sprintf.
func TestRestoreSQLHasNoQuotedSprintf(t *testing.T) {
	for _, file := range []string{"bootstrap.go", "flannel_bootstrap.go", "restore_sql.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "'%s'") || strings.Contains(line, "'%q'") || strings.Contains(line, "'{%s") || strings.Contains(line, "\"'\"+") {
				t.Errorf("%s:%d builds SQL with a quoted format verb: %s", file, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func mustJSONString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func sqlLiteralForTest(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

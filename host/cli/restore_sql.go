package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

// BUILD-16: the SQL `flynn-host bootstrap --from-backup` appends to the
// restored dump (and runs after the controller is up) quotes every value with
// sqlLiteral, never by putting it between quotes with Sprintf. Process and
// env keys in JSON paths are passed as ARRAY[...] elements, not spliced into
// a '{a,b}' path literal. The psql session runs with
// standard_conforming_strings on (the default, and what pg_dumpall sets), so
// a backslash in a literal is an ordinary character.

// restoreSQLFunctions defines the helper the restore SQL uses to set one key
// of a release env.
const restoreSQLFunctions = `
CREATE FUNCTION pg_temp.json_object_update_key(
  "json"          jsonb,
  "key_to_set"    TEXT,
  "value_to_set"  TEXT
)
  RETURNS jsonb
  LANGUAGE sql
  IMMUTABLE
  STRICT
AS $function$
     SELECT ('{' || string_agg(to_json("key") || ':' || "value", ',') || '}')::jsonb
       FROM (SELECT *
               FROM json_each("json"::json)
              WHERE "key" <> "key_to_set"
              UNION ALL
             SELECT "key_to_set", to_json("value_to_set")) AS "fields"
$function$;
`

// sqlLiteral is s as a standard-conforming SQL string literal: in single
// quotes with every single quote doubled. Nothing else is special in such a
// literal (standard_conforming_strings is on), so a semicolon, comment,
// backslash, dollar sign or newline stays part of the value.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// sqlJSON is v marshalled to JSON as a SQL string literal (for a jsonb
// column or a ::jsonb cast).
func sqlJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return sqlLiteral(string(data))
}

// psqlConnectLine is the psql meta-command that switches to database db. psql
// reads a meta-command to the end of the line, so a name with a line break
// (or a NUL) is refused rather than quoted.
func psqlConnectLine(db string) (string, error) {
	if db == "" || strings.ContainsAny(db, "\n\r\x00") {
		return "", fmt.Errorf("invalid controller PGDATABASE %q", db)
	}
	return "\\connect " + sqlLiteral(db) + "\n", nil
}

// currentReleaseOf is the release of the live app named app.
func currentReleaseOf(app string) string {
	return "(SELECT release_id FROM apps WHERE name = " + sqlLiteral(app) + " AND deleted_at IS NULL)"
}

func artifactSetSQL(a *ct.Artifact) string {
	return "uri = " + sqlLiteral(a.URI) +
		", type = 'flynn', manifest = " + sqlJSON(&a.RawManifest) +
		", hashes = " + sqlJSON(a.Hashes) +
		", size = " + strconv.FormatInt(a.Size, 10) +
		", layer_url_template = " + sqlLiteral(a.LayerURLTemplate) +
		", meta = " + sqlJSON(a.Meta)
}

// restoreArtifactSQL points the artifact of app's current release at the
// image this tarball ships.
func restoreArtifactSQL(app string, artifact *ct.Artifact) string {
	return `
UPDATE artifacts SET ` + artifactSetSQL(artifact) + ` WHERE artifact_id = (
  SELECT artifact_id FROM release_artifacts WHERE release_id = ` + currentReleaseOf(app) + `
);`
}

// restoreRedisArtifactSQL points the restored redis image artifact (by
// REDIS_IMAGE_ID or REDIS_IMAGE_URI) at the image this tarball ships and
// sets REDIS_IMAGE_ID on every release that names a REDIS_IMAGE_URI.
func restoreRedisArtifactSQL(redisImage *ct.Artifact) string {
	return `
UPDATE artifacts SET ` + artifactSetSQL(redisImage) + `
WHERE artifact_id = (SELECT (env->>'REDIS_IMAGE_ID')::uuid FROM releases WHERE release_id = ` + currentReleaseOf("redis") + `)
OR uri = (SELECT env->>'REDIS_IMAGE_URI' FROM releases WHERE release_id = ` + currentReleaseOf("redis") + `);
UPDATE releases SET env = jsonb_set(env, '{REDIS_IMAGE_ID}', to_jsonb((SELECT artifact_id::text FROM artifacts WHERE uri = ` + sqlLiteral(redisImage.URI) + `)), true)
WHERE env->>'REDIS_IMAGE_URI' IS NOT NULL;`
}

// removeStaleRecordsSQL deletes the job and volume records of the clusters
// the backup came from (everything created before startedAt).
func removeStaleRecordsSQL(startedAt string) string {
	t := sqlLiteral(startedAt)
	return `
DELETE FROM events WHERE object_type IN ('job', 'volume') AND created_at < ` + t + `;
DELETE FROM job_volumes USING job_cache WHERE job_volumes.job_id = job_cache.job_id AND job_cache.created_at < ` + t + `;
DELETE FROM job_volumes USING volumes WHERE job_volumes.volume_id = volumes.volume_id AND volumes.created_at < ` + t + `;
DELETE FROM job_cache WHERE created_at < ` + t + `;
DELETE FROM volumes WHERE created_at < ` + t + `;`
}

// dropProcessSingletonSQL removes SINGLETON from the env of process proc in
// app's current release, when that process has an env.
func dropProcessSingletonSQL(proc, app string) string {
	p := sqlLiteral(proc)
	return `
UPDATE releases SET processes = jsonb_set(processes, ARRAY[` + p + `, 'env'], (processes #> ARRAY[` + p + `, 'env']) - 'SINGLETON')
WHERE release_id IN ` + currentReleaseOf(app) + ` AND processes->` + p + ` ? 'env';`
}

// setSingletonSQL sets SINGLETON to value on the current release of apps.
func setSingletonSQL(value string, apps []string) string {
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = sqlLiteral(a)
	}
	return `
UPDATE releases SET env = jsonb_set(env, '{SINGLETON}', ` + sqlJSON(value) + `)
WHERE release_id IN (SELECT release_id FROM apps WHERE name IN (` + strings.Join(names, ", ") + `));
`
}

// formationScaleSQL sets process type typ to count in the formation of app's
// current release.
func formationScaleSQL(app, typ string, count int) string {
	return `
UPDATE formations SET processes = jsonb_set(processes, ARRAY[` + sqlLiteral(typ) + `], ` + sqlLiteral(strconv.Itoa(count)) + `)
WHERE release_id = ` + currentReleaseOf(app) + `;
`
}

// releaseEnvKeySQL sets env key to value on app's current release (with the
// pg_temp.json_object_update_key helper).
func releaseEnvKeySQL(app, key, value string) string {
	return `
UPDATE releases SET env = pg_temp.json_object_update_key(env, ` + sqlLiteral(key) + `, ` + sqlLiteral(value) + `)
WHERE release_id = ` + currentReleaseOf(app) + `;
`
}

// releaseEnvJSONSetSQL sets env key to value on app's current release with
// jsonb_set.
func releaseEnvJSONSetSQL(app, key, value string) string {
	return `
UPDATE releases SET env = jsonb_set(env, ARRAY[` + sqlLiteral(key) + `], ` + sqlJSON(value) + `)
WHERE release_id = ` + currentReleaseOf(app) + `;
`
}

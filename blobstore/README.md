# Blobstore

A simple, fast HTTP file service.

Blobstore provides a simple HTTP interface for reading, writing, and deleting
files. It is like a simpler S3. Object routes require the cluster `AUTH_KEY`
(HTTP Basic with an empty user, or `Authorization: Bearer`). `/.well-known/status`
and `HEAD /` are unauthenticated.

The blobstore fails closed. A release with no `AUTH_KEY`, `CONTROLLER_KEY`, or
`ACCESS_TOKEN_KEY` (an old backup restored with `--from-backup`, a hand-edited
release, a partial upgrade) answers every object request with `401` and logs
an error at start; status stays public. Only an explicit
`BLOBSTORE_ALLOW_UNAUTHENTICATED=1` (or `true`) serves objects without
credentials, and it is ignored while any key is set. `flynn-host
production:check` fails its `blobstore-auth` line in both cases.

All object operations fall under these three HTTP verbs:

 * PUT: write a file: `curl -u :$AUTH_KEY -X PUT -T /path/to/local/file
   http://blobstore.discoverd/path/to/remote/file`
 * GET: read a file: `curl -u :$AUTH_KEY http://blobstore.discoverd/path/to/remote/file`
 * DELETE: delete a file: `curl -u :$AUTH_KEY -X DELETE
   http://blobstore.discoverd/path/to/remote/file`

Files can be copied by setting the `Blobstore-Copy-From` header in a PUT
request. The source is not authorized separately, so only the cluster key (or a
cluster-admin token) may use this header. Signed build cache URLs (`BUILD_CACHE_URL`, `BUILD_CACHE_PUT_URL`) and
scoped `build:artifacts` tokens get `403` on any PUT that carries it.

A scoped `build:artifacts` token reaches only its own app's objects:
`/slugs/apps/<app-id>/layers/*`, `/slugs/apps/<app-id>/images/*`, and
`/tarreceive/apps/<app-id>/images/*` (GET, HEAD, PUT). Everything else,
including the app's Docker layers under `/tarreceive/apps/<app-id>/layers/`
(only tarreceive writes them, after checking the tar hash),
the older shared `/slugs/layers/`, `/slugs/images/`, and `/tarreceive/`
directories, needs the cluster key. Layer and image objects are
content addressed, so a scoped PUT over one that already exists is refused
with `409 Conflict` and nothing is written. The cluster key may still replace
any object.

Every PUT is bounded, whatever the credential. `MAX_OBJECT_SIZE` (default
`32g`) caps one object, counting the bytes already stored when the PUT
appends with `Blobstore-Offset`; a larger PUT gets `413 Request Entity Too
Large`. `APP_QUOTA` (default `100g`) caps the live bytes stored under one
app's names (`/slugs/apps/<app>/`, `/tarreceive/apps/<app>/` including the
Docker layers tarreceive stores for the app, `/<app>-cache.tgz`,
`/<app>-docker-cache.tgz`, `/repos/<app>.tar`); a PUT that would pass it gets
`507 Insufficient Storage`. A request with a
`Content-Length` over the limit is refused before a byte is stored; a chunked
one is cut off when it passes the limit and its upload is discarded, so an
overwritten object keeps its old content. `0` or `off` turns a limit off. See
[Upload limits](../docs/content/production.html.md#upload-limits).

A PUT over an existing object (without `Blobstore-Offset`) replaces it
atomically: if the upload fails or the client disconnects, the old content is
still served. On the S3, GCS, Azure, and MinIO backends the old object is
deleted only after the new upload is committed; if that delete fails it is left
for `flynn-blobstore cleanup`.

A PUT holds no database transaction, pool connection or row lock while its
body arrives. The body is written to a new object (a Postgres large object
written in 1 MiB chunks, or a new key in the bucket) that is recorded in the
`uploads` table; once the body is complete, one short transaction marks the
old row deleted and inserts the new one. A slow or stalled client therefore
does not block other writers of the same name or use up the pool. An append
(`Blobstore-Offset`) writes a new object with the old data followed by the new
data, and fails if the object was replaced meanwhile. A failed upload deletes
the object it wrote. An upload that makes no progress for an hour (its
blobstore instance stopped, or its idle timeout is off) is swept by the
server, which deletes its object.

A PUT body that sends nothing for `UPLOAD_IDLE_TIMEOUT` (default `60s`) is cut
off with `408 Request Timeout` and the connection is closed; nothing is stored
and an overwritten object keeps its old content. A body that keeps sending,
however slowly, is not cut off. The value is a duration (`90s`, `5m`) or a
number of seconds; `0` or `off` turns the timeout off. The server also gives
clients 30 seconds to send request headers and closes idle keep-alive
connections after 2 minutes. tarreceive and gitreceive use the same timeouts
(`pkg/uploadlimit`).

A DELETE marks the objects deleted, then removes them from their backend. If a
backend delete fails, or a backend named by a row is no longer configured, the
DELETE returns `500` and logs every error, and those objects stay listed for
`flynn-blobstore cleanup`. `cleanup` drops a row once its object is gone (an
object that is already missing counts as gone) and exits non-zero when any
object could not be deleted.

`flynn-blobstore cleanup-layers [--dry-run] [--grace=24h]` deletes the Docker
layers tarreceive stored (`/tarreceive/layers/` and
`/tarreceive/apps/<app>/layers/`, the squashfs and its `.json` config) that no
live artifact in the controller names, that are older than the grace period,
and that have no upload in progress. It reads the artifacts with
`CONTROLLER_KEY`, refuses to delete anything when that fails or the list is
empty, and checks age and uploads again under the upload locks just before
each delete, so a layer tarreceive reuses meanwhile (it re-puts the config) is
kept. With `LAYER_GC_INTERVAL` set (off by default) the server runs it on that
interval with `LAYER_GC_GRACE` (default `24h`).

`flynn-blobstore cleanup-legacy [--delete] [--grace=168h]` handles the legacy
shared directories that slugs and image manifests used before they were
stored per app: `/slugs/layers/`, `/slugs/images/` and `/tarreceive/images/`.
Those objects count toward no `APP_QUOTA`. They are not moved (a release's
artifact names them by URL); an object is deleted once no live artifact names
it, as a layer or as its URI, it is older than the grace period (default 7
days) and no upload of it is running, checked again under the upload locks
before each delete. Without `--delete` it only lists what it would delete. Like
`cleanup-layers` it refuses to delete anything when the controller cannot list
the artifacts.

A DELETE of `/a.b` removes `/a.b` and everything under `/a.b/`; the name, a
`GET /?dir=` directory and the `migrate --prefix` prefix are compared as
literal strings (no regular expression or `LIKE` pattern), so names with `.`,
`+`, `*`, `%`, `_` and the like only match themselves.

```shell
# create /file1.txt
echo data | curl -u :$AUTH_KEY -X PUT --data-binary @- http://blobstore.discoverd/file1.txt

# copy it to /file2.txt
curl -u :$AUTH_KEY -X PUT --header "Blobstore-Copy-From: /file1.txt" http://blobstore.discoverd/file2.txt

# read /file2.txt
curl -u :$AUTH_KEY http://blobstore.discoverd/file2.txt
data

```


Parent directories are automatically created, and directory indexes can be
accessed with a GET request to the root path with the `dir` query param:

```shell
# create 4 files in different directories
curl -u :$AUTH_KEY -X PUT --data-binary "data" http://blobstore.discoverd/foo.txt
curl -u :$AUTH_KEY -X PUT --data-binary "data" http://blobstore.discoverd/dir1/foo.txt
curl -u :$AUTH_KEY -X PUT --data-binary "data" http://blobstore.discoverd/dir2/foo.txt
curl -u :$AUTH_KEY -X PUT --data-binary "data" http://blobstore.discoverd/dir3/foo.txt

# list all top-level files and directories
curl -u :$AUTH_KEY http://blobstore.discoverd/
["/dir1/","/dir2/","/dir3/","/foo.txt"]

# list files in /dir1
curl -u :$AUTH_KEY http://blobstore.discoverd/?dir=/dir1
["/dir1/foo.txt"]

# list files in /dir2
curl -u :$AUTH_KEY http://blobstore.discoverd/?dir=/dir2
["/dir2/foo.txt"]
```

Right now, files are stored as large objects in PostgreSQL (the default) or on
the local filesystem. Production clusters can use S3, GCS, or Azure backends;
see [Production — Blobstore Backend](../docs/content/production.html.md#blobstore-backend).
Operators configure S3-compatible storage with `flynn-host blobstore:set` (and
inspect with `flynn-host blobstore:status`).

Flynn uses blobstore to store and retrieve Heroku-style slugs built with
[slugbuilder](../slugbuilder).

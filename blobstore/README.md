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
including the older shared `/slugs/layers/`, `/slugs/images/`, and
`/tarreceive/` directories, needs the cluster key. Layer and image objects are
content addressed, so a scoped PUT over one that already exists is refused
with `409 Conflict` and nothing is written. The cluster key may still replace
any object.

Every PUT is bounded, whatever the credential. `MAX_OBJECT_SIZE` (default
`32g`) caps one object, counting the bytes already stored when the PUT
appends with `Blobstore-Offset`; a larger PUT gets `413 Request Entity Too
Large`. `APP_QUOTA` (default `100g`) caps the live bytes stored under one
app's names (`/slugs/apps/<app>/`, `/tarreceive/apps/<app>/`,
`/<app>-cache.tgz`, `/<app>-docker-cache.tgz`, `/repos/<app>.tar`); a PUT that
would pass it gets `507 Insufficient Storage`. A request with a
`Content-Length` over the limit is refused before a byte is stored; a chunked
one is cut off when it passes the limit and its transaction rolls back, so an
overwritten object keeps its old content. `0` or `off` turns a limit off. See
[Upload limits](../docs/content/production.html.md#upload-limits).

A PUT over an existing object (without `Blobstore-Offset`) replaces it
atomically: if the upload fails or the client disconnects, the old content is
still served. On the S3, GCS, Azure, and MinIO backends the old object is
deleted only after the new upload is committed; if that delete fails it is left
for `flynn-blobstore cleanup`.

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

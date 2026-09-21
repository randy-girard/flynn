# Blobstore

A simple, fast HTTP file service.

Blobstore provides a simple HTTP interface for reading, writing, and deleting
files. It is like a simpler S3. Object routes require the cluster `AUTH_KEY`
(HTTP Basic with an empty user, or `Authorization: Bearer`). `/.well-known/status`
and `HEAD /` are unauthenticated. All object operations fall under these three
HTTP verbs:

 * PUT: write a file: `curl -u :$AUTH_KEY -X PUT -T /path/to/local/file
   http://blobstore.discoverd/path/to/remote/file`
 * GET: read a file: `curl -u :$AUTH_KEY http://blobstore.discoverd/path/to/remote/file`
 * DELETE: delete a file: `curl -u :$AUTH_KEY -X DELETE
   http://blobstore.discoverd/path/to/remote/file`

Files can be copied by setting the `Blobstore-Copy-From` header in a PUT
request:

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

Flynn uses blobstore to store and retrieve Heroku-style slugs built with
[slugbuilder](../slugbuilder).

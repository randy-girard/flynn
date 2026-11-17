---
title: Docker
layout: docs
toc_min_level: 2
---

# Docker

Flynn can run applications from Docker images in two ways:

- **`flynn docker:push`** — push a pre-built image from your machine
- **`git push` with the container stack** — build from a `Dockerfile` on the
  cluster (see [Container stack](#container-stack))

Both paths import images through the built-in `tarreceive` service and share the
same image import and release logic in the cluster.

## Push a pre-built image

Run the following to push a Docker image to `tarreceive` and deploy it:

```
$ flynn -a APPNAME docker:push IMAGE
```

where `APPNAME` is the name of an existing Flynn app and `IMAGE` is a reference
to a Docker image which is available to the local `docker` CLI (in other words,
an image which appears in the output of `docker images`).

The release process type is the image `ENTRYPOINT` plus `CMD` (the same argv
used to start the container). The dashboard Resources tab shows that command
for each process.

## Container stack

To build from a `Dockerfile` on the server instead of using buildpacks, switch
the app to the container stack and deploy with `git push`:

```
$ flynn stack:set container
$ git push flynn master
```

The cluster builds the image with [BuildKit](https://github.com/moby/buildkit)
and imports it using the same pipeline as `flynn docker:push`.

Switch back to buildpack deploys at any time:

```
$ flynn stack:set heroku-24
```

Optional configuration:

- `flynn env:set DOCKERFILE=path/to/Dockerfile` — use a non-default Dockerfile.
  The build context stays the repository root.
- `flynn limit:set dockerbuilder memory=4GB` — raise build memory for large images (cluster controller key; see [Apps — Limits](apps.md#limits))

`flynn docker:push` does not read `flynn.yml`. A missing `flynn.yml` keeps the
single root `Dockerfile` (or `DOCKERFILE`) and one image.

## flynn.yml

A container-stack git push reads `flynn.yml` at the repository root. This is
the container manifest. Provisioning of an existing app still belongs to
[`flynn-app.json`](app-manifest.md). `flynn.yml` `setup` runs only on the
first untouched deploy. `flynn stack:set container` writes a release with no
image and no processes, and that release still counts as untouched. The next
push does not create another database. When
the repo also has `flynn-app.json`, that file owns provisioning and the push
prints `flynn.yml setup ignored`.

A heroku-24 app whose repo contains `flynn.yml` fails before the buildpack
build:

```
flynn.yml requires the container stack (flynn stack:set container)
```

```yaml
setup:
  addons:
    - plan: heroku-postgresql
      as: DATABASE
    - type: redis
  config:
    S3_BUCKET: my-example-bucket
build:
  docker:
    web: Dockerfile
    worker:
      dockerfile: worker/Dockerfile
      target: production
    release:
      dockerfile: Dockerfile
      target: builder
  config:
    RAILS_ENV: development
    FOO: bar
release:
  image: release
  command:
    - ./deployment-tasks.sh
run:
  web: bundle exec puma -C config/puma.rb
  worker: python myworker.py
  asset-syncer:
    command:
      - python
      - asset-syncer.py
    image: worker
```

`build.docker` is required. A value is a Dockerfile path, or
`{dockerfile, target}`. The path must stay inside the repo. `..`, an absolute
path, and a symlink that leaves the repo fail before the build. The build
context is the directory that contains that Dockerfile. `target` builds only
that stage. Two keys with the same Dockerfile and target share one image.
One `flynn.yml` may build at most 8 images. The file itself must be 1 MiB or
smaller.

`build.config` values are strings. A number or boolean is rejected. Those
values are Docker build args for every image. They are not copied onto the
app env, and the app's runtime env is not passed into the build.

`setup.addons` `plan` or `type` runs on that first deploy. `heroku-postgresql`
is postgres, `heroku-redis` and `openredis` are redis, `mongolab` and
`mongodb` are mongodb, and `jawsdb`, `cleardb`, and `jawsdb-maria` are mysql.
The tier after a colon is ignored (`heroku-postgresql:mini` is still
postgres). Any other plan or type must be a name from `flynn addons:types`.
An unknown name fails before the build. `as` is the attachment name.
`options.runtime` defaults to `small` when the add-on has runtimes.
`setup.config` is string config on that same first deploy. It cannot repeat
an add-on URL such as `DATABASE_URL`, `REDIS_URL`, or `<as>_URL`.

`run` is optional. When it is omitted, each built image except `release`
becomes a process of the same name and uses the image `ENTRYPOINT` and `CMD`.
A string is a shell command (`sh -c`) unless the image entrypoint already
consumes the command. An object has `command` (an argv, required) and
`image` (optional). A missing image fails before the build. `run` replaces
`CMD`. It does not change scale. `web` listens on `PORT` (default 8080) and
is registered for the included route. Other process types are not given that
service.

`release.command` is an argv, not a shell string. It runs once after the
images are imported and before traffic moves, with the app runtime env,
including add-on URLs. Its stdout is on the git push. `release.image`
selects the image. The default is `web` when that image was built, otherwise
the only built image. When `release` is omitted, no release command runs. A
Procfile `release` line is ignored while `flynn.yml` is the manifest. A
`build.docker` key named `release` is that one-shot image and is not scaled.

One image keeps a single artifact on the release. More than one built image
sets an artifact id on each process. Scaling `worker` starts the worker
image. The release still lists every image so blob GC sees them.

## Routing

Flynn automatically registers `https://APPNAME.$CLUSTER_DOMAIN` (and HTTP on
the same hostname). Listen on `$PORT`. The default `web` process gets
`PORT=8080`.

## Example (pre-built image)

Here is an example of deploying the [Flynn Node.js example app](https://github.com/flynn-examples/nodejs-flynn-example)
using `flynn docker:push`.

Clone the git repository:

```
$ git clone https://github.com/flynn-examples/nodejs-flynn-example.git
$ cd nodejs-flynn-example
```

Build the Docker image:

```
$ docker build -t nodejs-flynn-example .
```

Create an app:

```
$ flynn apps:create --remote "" nodejs
Created nodejs
```

_NOTE: the `--remote ""` flag prevents Flynn trying to configure the local
git repository with a `flynn` remote, something which is useful only when
deploying with `git push` rather than `flynn docker:push`._

Push the Docker image:

```
$ flynn -a nodejs docker:push nodejs-flynn-example
deploying Docker image: nodejs-flynn-example
exporting image with 'docker save nodejs-flynn-example'
650.67 MB 107.82 MB/s 6s
uploading layer 8fad67424c4e7098f255513e160caa00852bcff347bc9f920a82ddf3f60229de
123.29 MB 22.98 MB/s 5s
uploading layer 86985c679800f423275a0ea3ad540e9b7f522dcdcd65ec2b20f407996162f2e0
43.35 MB 24.49 MB/s 1s
uploading layer 6e5e20cbf4a7246b94f7acf2a2ceb2c521e95daca334dd1e8ba388fa73443dfe
121.03 MB 11.38 MB/s 10s
uploading layer ff57bdb79ac820da132ad1fdc1e2d250de5985b264dbdf60aa4ce83a05c4da75
313.16 MB 7.18 MB/s 43s
uploading layer 0e0b4ee1c6dc1f57a46071fc075ecf66b3164e637096197f10b89b6086942c7a
344.50 KB 632.22 KB/s 0s
uploading layer 33aed7748ee38dd620489fce6366fab070591e3b5ec276b5f505ed957772d7bc
132.00 KB 3.73 MB/s 0s
uploading layer 3b227e2efc63768cd4c62d5e7e6678b98064bf5bdbc40fc845913e970ccc5610
44.96 MB 13.26 MB/s 3s
uploading layer 984a57f9e68ea548ace7b655095d109a94850c918d10707ac4c922c9d9778136
4.25 MB 36.01 MB/s 0s
uploading layer 38a320798e65a1b2faed502dcac756feddcc85b3e83d72d7a57998fc992a2564
4.00 KB 159.07 KB/s 0s
uploading layer 148577a474a5c57422278e836bf204288fae7bf9f3ab5d6643666c30abb9bd87
5.50 KB 150.91 KB/s 0s
uploading layer 7779527ad11c4b4f1edb62e7f1391ee6cd82df898b8e96a16822ae79124669e2
108.00 KB 2.22 MB/s 0s
Docker image deployed, scale it with 'flynn scale app=N'

```

You now have a release with an `app` process which runs using the pushed image
and has an `ENTRYPOINT`, `CMD` and `ENV` as taken from the Docker image's
config.

If this is the first deploy of the app, scale the `app` process to start it:

```
$ flynn -a nodejs scale app=1
scaling app: 0=>1

16:24:31.397 ==> app 4a7319af-af2c-4fe1-9a9a-2dd4d5bd3765 pending
16:24:31.398 ==> app host0-4a7319af-af2c-4fe1-9a9a-2dd4d5bd3765 starting
16:24:31.527 ==> app host0-4a7319af-af2c-4fe1-9a9a-2dd4d5bd3765 up

scale completed in 140.63784ms
```

The `app` process is registered in service discovery for the **router**. User
jobs cannot reach each other on the overlay (including
`APPNAME-web.discoverd`). Reach the app through a route you have added, or the
HTTP URL Flynn prints after deploy:

```
$ curl http://nodejs.1.localflynn.com
Hello from Flynn on port 8080 from container 4a7319af-af2c-4fe1-9a9a-2dd4d5bd3765
```

Datastore connections from the app still use the URLs Flynn injected
(`REDIS_URL` → `leader.<redis-app>.discoverd`, tenant `DATABASE_URL` →
`leader.<postgres-app>.discoverd`, and so on). Only those attached leader
names resolve. Internal names such as `postgres.discoverd` or
`postgres-api.discoverd` are not resolvable from user jobs.

The app can also be reached externally via the automatically registered route
`http://APPNAME.$CLUSTER_DOMAIN`.

## Example (container stack)

With a `Dockerfile` committed in your repository:

```
$ flynn apps:create myapp
$ flynn stack:set container
$ git push flynn master
-----> Building myapp from Dockerfile...
-----> Uploading image...
-----> Creating release...
=====> Application deployed
=====> URLs
       https://myapp.demo.localflynn.com
       http://myapp.demo.localflynn.com
$ flynn scale app=1
```

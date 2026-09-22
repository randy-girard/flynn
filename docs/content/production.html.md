---
title: Production
layout: docs
toc_min_level: 2
---

# Production

We've designed Flynn to be very easy to get up and running with quickly.
However, there are a variety of things that should be considered as you start
using Flynn more.

## Cluster Requirements

At least three hosts are required to deploy Flynn in a highly available
configuration. We do not recommend single-node clusters for production use.
A three-host cluster will withstand the loss of one node with little to no
impact on availability.

All members of the initial cluster participate in Raft consensus voting, hosts
started after the initial bootstrap act as proxies to the consensus cluster. We
recommend starting with three or five hosts and adding more hosts when
necessary.

Each host should have a minimum of 2GB of memory, and inter-host network packets
should have a latency of less than 2ms. Deploying a single Flynn cluster across
higher latency WAN links is not recommended, as it can have a significant impact
on the stability of cluster consensus.

## TLS / Let's Encrypt

Bootstrap uses a self-signed certificate. For production, configure ACME on a
cluster host so the dashboard, controller, and `--auto-tls` app routes get
trusted certificates:

```text
$ sudo flynn-host acme:configure --email=admin@example.com --agree-tos
$ sudo flynn-host acme:enable-system-routes
$ flynn cluster:refresh --clear
```

`CLUSTER_DOMAIN` and a wildcard must resolve to the cluster (HTTP-01 on ports
80 and 443). See [Apps — HTTPS](apps.md#https).

## Storage

Flynn uses ZFS to store data. By default, a ZFS pool is created in a sparse file
on top of the existing filesystem at `/var/lib/flynn/volumes`. We don't
recommend keeping this configuration in production, as it is not as reliable as
dedicating whole disks to the ZFS pool.

### Custom ZFS pool

The Flynn install script can be used to create the ZFS pool on the device of
your choice instead of a sparse file, using the `--zpool-create-device` flag:

```text
$ ./install-flynn --zpool-create-device /dev/sdb --zpool-create-options "-f"
```

If you already have a Flynn cluster running, you can move the existing ZFS pool
off of the sparse file by first attaching your disk as a mirror, then detaching
the sparse file after it has been replicated to the new disk:

```text
# Attach /dev/sdb1 (specify your disk instead of sdb1) to the flynn-default ZFS pool
$ sudo zpool attach flynn-default /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev /dev/sdb1

# Wait for the resilver to copy all data onto the newly added disk
$ sudo zpool status flynn-default
  pool: flynn-default
 state: ONLINE
  scan: resilvered 59.7M in 0h0m with 0 errors on Mon Nov  2 04:02:58 2015
config:

	NAME                                                          STATE     READ WRITE CKSUM
	flynn-default                                                 ONLINE       0     0     0
	  mirror-0                                                    ONLINE       0     0     0
	    /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev  ONLINE       0     0     0
	    sdb1                                                      ONLINE       0     0     0

errors: No known data errors

# Detach the sparse file from the ZFS pool and delete it
$ sudo zpool detach flynn-default /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev
$ sudo rm /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev
```

### Image cache

Job layers live on the host root filesystem (`/var/lib/flynn/layer-cache` and
per-job image dirs), not in the ZFS pool. flynn-host deletes unused files there
on a timer and when free space is low. That does not replace a dedicated ZFS
pool, and leftover datasets still need `flynn-host volume:gc` or a cluster
update (which runs the same GC before pulling images).

## Blobstore Backend

Flynn stores binary blobs like compiled applications, git repo archives,
buildpack caches, and Docker image layers using the blobstore component. The
blobstore supports multiple backends: Postgres, [Amazon
S3](https://aws.amazon.com/s3/), [Google Cloud
Storage](https://cloud.google.com/storage/), and [Microsoft Azure
Storage](https://azure.microsoft.com/en-us/services/storage/blobs/).

By default, the blobstore uses the built-in Postgres appliance to store these
blobs. This works well for light workloads and is the default configuration
because it allows Flynn to be deployed anywhere without any external service
dependencies.

We recommend using one of the external blobstore backends listed below for
production, as they are more reliable than the Postgres backend and have
predictable performance without consuming disk space on the Flynn hosts.

### Amazon S3

To migrate to the S3 backend, you first need to provision a new bucket and
create credentials for it. In AWS IAM, create a user with access credentials and
add a policy that looks like this:

```text
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "s3:DeleteObject",
                "s3:GetObject",
                "s3:ListBucket",
                "s3:PutObject",
                "s3:ListMultipartUploadParts",
                "s3:AbortMultipartUpload",
                "s3:ListBucketMultipartUploads"
            ],
            "Resource": [
                "arn:aws:s3:::flynnblobstore",
                "arn:aws:s3:::flynnblobstore/*"
            ]
        }
    ]
}
```

Don't forget to save the credentials when creating the user and set the bucket
name in the `Resource` section before adding the policy.

After creating the S3 bucket and credentials, configure the blobstore to use it as
the backend with bucket, region, and access credentials:

```text
flynn -a blobstore env:set BACKEND_S3MAIN="backend=s3 region=us-east-1 \
bucket=flynnblobstore access_key_id=$AWS_ACCESS_KEY_ID \
secret_access_key=$AWS_SECRET_ACCESS_KEY"

flynn -a blobstore env:set DEFAULT_BACKEND=s3main
```

If the credentials are invalid, the first command will fail, and you can check the
logs with `flynn -a blobstore log`.

Finally, migrate the existing blobs from Postgres to S3 and remove them from
Postgres:

```text
flynn -a blobstore run /bin/flynn-blobstore migrate --delete
```

Or if you're on a version of Flynn older than v20160924.0:

```text
flynn -a blobstore run /bin/flynn-blobstore-migrate --delete
```

### Google Cloud Storage

To migrate to the Google Cloud Storage backend, you first need to create a new
[Service Account in
IAM](https://console.cloud.google.com/iam-admin/serviceaccounts). The account
does not need any roles, but make sure you create a private key and download the
JSON version. After creating the account, create a Cloud Storage bucket and add
the service account ID as a user with Owner access to the bucket permissions.

After creating the Cloud Storage bucket and credentials, configure the blobstore
to use it as the backend, with the bucket name and key file:

```text
flynn -a blobstore env:set BACKEND_GCSMAIN="backend=gcs bucket=flynnblobstore" \
BACKEND_GCSMAIN_KEY="$(cat Project-7633a787c43f.json)"

flynn -a blobstore env:set DEFAULT_BACKEND=gcsmain
```

If the credentials are invalid, the first command will fail, and you can check the
logs with `flynn -a blobstore log`.

Finally, migrate the existing blobs from Postgres to Cloud Storage and
remove them from Postgres:

```text
flynn -a blobstore run /bin/flynn-blobstore migrate --delete
```

### Microsoft Azure Storage

To migrate to the Azure Storage backend, you first need to create a storage
account and container. After setting those up, you should have an account name,
account key, and container name, which can be used to configure the backend:

```text
flynn -a blobstore env:set BACKEND_AZUREMAIN="backend=azure account_key=xxx \
account_name=yyy container=flynnblobstore"

flynn -a blobstore env:set DEFAULT_BACKEND=azuremain
```

If the credentials are invalid, the first command will fail, and you can check the
logs with `flynn -a blobstore log`.

Finally, migrate the existing blobs from Postgres to Azure Storage and
remove them from Postgres:

```text
flynn -a blobstore run /bin/flynn-blobstore migrate --delete
```


## DNS and Load Balancing

Flynn has a built-in router that handles all incoming HTTP, HTTPS and TCP
traffic. It is lightweight and runs on every Flynn host, this allows easy
deployment of Flynn without requiring another load balancer in front of Flynn.

We recommend running Flynn with low-TTL round-robin DNS A records pointing at
each host. For automatic failover, health checks should be configured that
automatically remove a host's A record if it is unhealthy.

Alternatively, a TCP load balancer for ports 80 and 443 can be configured in
front of the Flynn router, this may increase overhead and complexity but can
make sense in some environments.

## Firewalling

A firewall preventing external access must always be configured on or in front
of Flynn hosts, only these ports should be allowed:

* 80 (HTTP)
* 443 (HTTPS)
* 3000 to 3500 (user-defined TCP services, optional — datastore exports and
  other TCP routes)

Internal cross-host cluster communication happens on a variety of UDP and TCP
ports and should not be restricted.

The installer owns public 22/80/443 and private cluster CIDRs. After install,
`flynn-host firewall` shows managed UFW rules. Use it to allow a peer that is
not in a private CIDR, or to open an extra TCP port (a TCP route or an exported
datastore). User and build jobs are separately blocked from those host ports and
from discoverd by overlay iptables, even though UFW allows the cluster CIDR for
host-to-host traffic.

```text
sudo flynn-host firewall
sudo flynn-host firewall:peer:add <ip>
sudo flynn-host firewall:expose 3001
sudo flynn-host firewall:sync
```

`firewall:peer:remove` and `firewall:unexpose` drop those extra allows.
`--peer-ips` and `--ports` on `firewall:sync` seed extra allows stored for later
syncs.

### Exporting datastores

HTTP apps get a stable hostname on the cluster domain. Datastores use the same
idea on a TCP port in 3000–3500:

```text
flynn -a myapp resource:expose postgres
# prints: sudo flynn-host firewall:expose PORT
sudo flynn-host firewall:expose 3001   # on every host
```

`resource:expose` creates a leader TCP route (default hostname
`<service>.<cluster-domain>`, for example `postgres.example.com`) with TLS
passthrough so Postgres/MySQL native SSL still works. `--auto-tls` switches the
route to TLS terminate and attaches a Let's Encrypt cert (HTTP-01 still uses
ports 80/443). See [Databases](databases.html.md) and each engine page.

`flynn-host` also opens live TCP route ports on its 15s firewall sync (same
UFW `flynn-expose` rules). `firewall:expose` pins the port in Extra.Ports so it
stays open if the controller is unreachable. After `resource:unexpose`, run
`sudo flynn-host firewall:unexpose PORT` on each host.

Treat an exported datastore port as public. Prefer TLS, restrict who can reach
3000–3500, and use a VPN when you can. In-cluster apps keep using the
`*.discoverd` names in `DATABASE_URL` / `REDIS_URL` / etc.; they do not need a
TCP route.

Outbound Internet access is required to deploy apps using many of the default
buildpacks.

## Automation

Installation and management of Flynn clusters can be automated using a variety
of existing tools. We recommend using tools that you are familiar with to create
a repeatable system for deploying Flynn clusters. This will allow you to quickly
start new clusters for testing, updates, and failure remediation.

When possible, we recommend deploying immutable infrastructure and deploying new
clusters for updates and major configuration changes.

## Backups

Flynn supports full-cluster backup/restore as well as export/import of
individual applications (including their databases).

### Cluster Backup

To take a full-cluster backup, run `flynn-host backup --file backup.tar`.
A file named `backup.tar` is created with the data needed to stand up a new
cluster: `flynn.json` (discoverd/flannel/postgres/controller, plus MariaDB and
MongoDB if they were running), `plugins.json` (which plugins were installed),
a full `pg_dumpall` of Postgres (controller, blobstore files including plugin
image layers, and every app Postgres database), and MariaDB/MongoDB dumps
when those appliances are scaled above zero. If the current postgres release
has no formation row yet (seen after an updater deploy), backup copies
process counts from another scaled formation on that app instead of failing.
Restore does **not** re-run
`flynn-host plugin:install`; plugin apps come back with postgres. Redis, Kafka,
and ClickHouse keep data on volumes that are **not** included; after restore
those engines come back empty. App slugs and container images stored in the
blobstore (Postgres) are restored.

The Vagrant upgrade smoke (`script/vagrant-upgrade-smoke.sh`) exercises this
path after the in-place `--force` updates: backup, `install --clean`, then
`flynn-host bootstrap --from-backup`.

### Cluster Restore

To restore from a full-cluster backup, follow [the manual installation
instructions](installation/manual.md) and modify the `flynn-host bootstrap`
command to include an extra flag: `--from-backup backup.tar`. The cluster size
does not need to be the same, but the `--min-hosts` flag and cluster discovery
flag should be specified. The `CLUSTER_DOMAIN` variable is ignored, the domain
of the previous cluster will be used.

Restoring a singleton backup onto `--min-hosts 3` keeps the backup's
`SINGLETON` flag and process counts so Postgres/MariaDB/MongoDB can elect a
leader during wait/dump. After controller is up, the scheduler promotes those
appliances to HA (new `SINGLETON=false` release at scale 1, then data process
scale 3) once three hosts are active. Restoring onto a single host still
forces singleton scale. Growing a live singleton cluster to three hosts uses
the same promotion path.

### App Export

To export a single app, run `flynn -a APPNAME apps:export --file app.tar`. A file
named `app.tar` will be created with the app configuration and image, along with
a copy of all data stored in associated databases. The app export can be
restored to the same cluster under a different name, or a different Flynn
cluster.

### App Import

To import an app, run `flynn apps:import --file app.tar`. This will create a new app
on the cluster, import the configuration, create databases, and import the data
from the exported app. If you'd like to provide a new name for the app, the
`--name` flag may be specified. By default a new route is created based on the
app name and the cluster domain. To import the old routes in addition to the new
route, add the `--routes` flag.

## SSH Client Key

Some apps require a SSH private key to download dependencies or submodules while
deploying. Flynn supports configuring a single SSH client key for the platform
that will be used whenever SSH is used during builds triggered by `git push`.

To configure the keypair, set the `SSH_CLIENT_KEY` and `SSH_CLIENT_HOSTS`
environment variables on the built-in `gitreceive` app:

```text
flynn -a gitreceive env:set SSH_CLIENT_HOSTS="$(ssh-keyscan -H github.com)"\
   SSH_CLIENT_KEY="$(cat ~/.ssh/id_rsa)"
```

## Monitoring

Flynn provides a status endpoint over HTTP that exposes the health of system
components at `http://status.$CLUSTER_DOMAIN` (for example,
`https://status.1.localflynn.com`). The status endpoint returns a status code
along with a more detailed JSON response. If any core components are unhealthy,
the HTTP status will be 500.

Requests to the status endpoint require a `key` parameter (example:
`https://status.1.localflynn.com/?key=$AUTH_KEY`) when they come from IP
addresses that are not reserved for private use.

The `$AUTH_KEY` may be retrieved with this command:

```text
flynn -a status env:get AUTH_KEY
```

### OpenTelemetry (Grafana, Alloy, collector)

OpenTelemetry metrics are an **optional plugin**. Clusters that do not install
it do not export OTLP. After `flynn-host plugin:install otel`,
`flynn-host otel` forwards **host metrics** (CPU, memory, disk, load, job
counts) to any OTLP/HTTP endpoint (`http://host:4318`, Grafana Alloy, the
OpenTelemetry Collector, Grafana Cloud OTLP). Path `/v1/metrics` is appended.
The plugin `/exporters` API requires the cluster key (HTTP Basic with an empty
username, or Bearer). `flynn-host otel` sends it automatically
(`CONTROLLER_KEY` / `AUTH_KEY`, else the controller discoverd `AUTH_KEY`).
Collector `--auth bearer` / `--auth basic` on `otel:add` are OTLP exporter
headers, not this Flynn API.

```text
sudo flynn-host plugin:install otel

# Metrics to a local collector
sudo flynn-host otel:add http://alloy.example:4318

# Grafana Cloud (or any collector that needs a header)
sudo flynn-host otel:add --header "Authorization: Bearer TOKEN" https://otlp.grafana.net/otlp
sudo flynn-host otel:add --insecure https://collector.example:4318

sudo flynn-host otel
sudo flynn-host otel:remove <id>
```

Job logs stay on syslog. `flynn log-sink` is **per app**. `flynn-host log-sink`
is cluster-wide and accepts `--scope` / `--app` filters:

```text
sudo flynn-host log-sink:add syslog --scope system syslog://logs.example:514/
flynn -a myapp log-sink:add syslog syslog://logs.example:514/
```

The dashboard still shows live metrics for operators who are already logged in.
`flynn-host metrics` prints a live host snapshot without the dashboard.
`flynn-host alert` stores cluster rules (CPU, memory, disk, load, running jobs)
in the dashboard plugin; `flynn alert` and `flynn metrics` are the app-scoped
equivalents.

## Debugging

Flynn is a self-hosting system, this allows you to use the `flynn` and
`flynn-host` tools to introspect, configure, and debug the system. All
components with the exception of the `flynn-host` daemon appear as and can be
managed as apps in Flynn.

### Retrieving logs

Logs can be retrieved using several methods.

The `flynn -a $APP_NAME log` command will retrieve up to the last 10,000 lines
logged by all app processes and can also follow the stream as new log lines are
emitted.

The `flynn-host log $JOB_ID` command on a server will retrieve the logs for
a specific job. Pass an app name instead (`flynn-host log dashboard`) to get
logs from every job of that app; `-a` includes jobs that are no longer
running. `--follow` streams new lines from running jobs. A list of all jobs,
including those that are no longer running can be retrieved with the `flynn-host
ps -a` command.

Flynn stores logs from jobs in `/var/log/flynn` on each host. There is one log
file for jobs from each app that has been run on the host. All logs from jobs
tagged with the same app ID will go into a file named
`/var/log/flynn/$APP_ID.log`. When a log file reaches 100MB in size, it is
rotated and a new file is created. One previous rotated log is kept, for a total
of a maximum 200MB of logs per app per host.

systemd manages the `flynn-host` daemon. Logs are in `/var/log/flynn/flynn-host.log`
(also `journalctl -u flynn-host`).

The `flynn-host collect-debug-info` command will collect information about the
system it is run on along with recent logs from all apps and the `flynn-host`
daemon. By default it uploads these logs to [GitHub's
Gist](https://gist.github.com) service, but they can also be saved to a local
tarball with the `--tarball` flag.

### Internal Databases

The `controller`, `router`, and `blobstore` components store data in a
PostgreSQL cluster managed by Flynn.

`flynn -a $APP_NAME pg:psql` is **not** a public console. It is a controller
API call (`flynn run` of `psql`) and is authorized like every other `flynn`
command:

* **Cluster operators** who registered with `flynn cluster:add` / `flynn-host
  cli-add-command` have the controller key. That key is cluster-admin (treat it
  like root). They can open a console on `controller`, `blobstore`, and other
  system apps. Do not put that key in application config or share it with
  dashboard-only users.
* **Dashboard users** (`flynn login`) only act on apps they were granted. The
  Team role (View=`app:read`, Deploy=`app:deploy`, Manage=`app:write`,
  Admin=`app:admin`, or a custom combination of function/action grants) is what the controller enforces
  on every CLI command. They can `flynn pg:psql` their own app's database when
  the role includes write-level access. They cannot open a console on
  `controller`, `blobstore`, `postgres`, or other system apps, even if a grant
  names those apps.
* **Application jobs** cannot reach `postgres-api`, `controller`, or
  `blobstore` on the overlay. They may TCP to `leader.postgres.discoverd` only
  to use the `DATABASE_URL` Flynn provisioned. Each Postgres role can CONNECT
  only to its own database; `PUBLIC` CONNECT is revoked, so one app's user
  cannot open another app's (or the controller's) database.

User-app consoles: `flynn -a myapp pg:psql`. Platform databases: cluster key
only, `flynn -a controller pg:psql` / `flynn -a blobstore pg:psql`.

## Updating

There are two ways to update Flynn: in-place and backup/restore. The in-place
updater is new and we do not consider it to be stable. The most reliable
update method is backup/restore.

### Backup/Restore

The backup/restore update method involves taking a full backup of the cluster
and restoring it to a new cluster with the new version of Flynn.

1. Take a backup of the cluster with `flynn-host backup --file backup.tar`.
2. Install the new version of Flynn on a new cluster by following [the manual
   installation instructions](installation/manual.md) up to but not including
   the bootstrap step.
3. Run the `flynn-host bootstrap` command from the installation guide with an
   added flag pointing at the cluster backup file: `flynn-host bootstrap
   --from-backup backup.tar`
4. Update the DNS records that point at the old cluster to point at the new
   cluster.

### In-place update

The in-place updater is new and could cause cluster failure. We recommend taking
a full backup of the cluster first with `flynn-host backup`. There is almost
zero downtime during the cluster update, however database clusters may be
unavailable for a few seconds while they are updated.

To perform an in-place update of **binaries on one host** (no other nodes, no container image rollout), run `flynn-host update`. A newer GitHub release always continues past the flynn-host re-exec (init, CLI, daemon restart, and—on a single-node cluster—image rollout) without `--force`. `--force` repeats an update when the host is already on that version. `flynn-host update --check` only reports whether a newer GitHub release exists; that lookup is cached in `~/.flynn/update-check-cache.json` for one hour (same file, TTL, and `FLYNN_UPDATE_CHECK_*` variables as `flynn update --check`). `--check --force` or `FLYNN_UPDATE_CHECK_TTL=0` refreshes the cache. Installing still fetches a live release.

To update **every host**—push new `flynn-host` binaries to all peers, pull image layers on each node, and deploy system apps—run `flynn-host update --all-nodes` (after taking a backup as recommended above). The router holds host ports 80/443, so the updater stops the old router before starting the replacement, one host at a time on a multi-node cluster (other nodes keep serving; a single-node cluster still has a brief HTTP/HTTPS blip). Redis appliances use the same one-down-one-up strategy so `/data` is reused. Use `flynn-host update --skip-images` to roll binaries out everywhere without touching images. After system apps, the updater refreshes slugrunner on git/slug user apps that already have a release. Container-stack git deploys and `flynn docker:push` apps keep their own image artifacts (they are not rewritten to slugrunner, which would drop files like `/start.sh`). Apps created in the dashboard or with `flynn apps:create` that have never been deployed are skipped; a missing release on a required system app still fails the update. Missing cluster secrets (`AUTH_KEY` / `CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY`, blobstore `ACCESS_TOKEN_KEY`) are copied onto system-app releases from controller/postgres/gitreceive so an upgrade does not require a manual `env:set`. Plugin apps get the same backfill on `flynn-host plugin:update`.

When the update will pull container images, it first garbage-collects unused volumes (the same keep rules as `flynn-host volume:gc`: running jobs, controller-tracked volumes, and system images) and leftover image cache, then requires at least 5 GiB free on each host. Persistent database volumes the scheduler still tracks are not deleted. If a host is still short of space after that, the update stops before changing binaries so the cluster is not left half-upgraded.

## Adding Hosts

Hosts may be added to an existing cluster by running `flynn-host init` with the
discovery token or list of host IPs that was used to start the cluster.

Care should be taken to ensure that the same version of Flynn is installed on
all hosts. The installed version of Flynn can be checked with `flynn-host
version`, and the version to install can be specified by setting the `--version`
CLI flag to the desired version when running the install script.

# Replacing Hosts

If a member of the cluster that is participating in the consensus set becomes
permanently unavailable, it must be replaced in order to restore fault
tolerance. If you have already added additional hosts to your cluster beyond the
members of the consensus set, you can promote one of these peers to the
consensus set. Otherwise you will first need to start a new host and join it to
the cluster as described in the Adding Hosts section. You can check the Raft
consensus status of your cluster hosts by running `flynn-host list`. If the host
is a member its status will be displayed as `peer`. If not, it will be listed as
`proxy`.

The first step is to demote the old peer from the consensus set. If the peer is
already unavailable, use the `--force` flag. If you are preemptively replacing
a peer that is currently working, the force flag should not be used.

To demote the peer, run `flynn-host demote --force $PEER_IP`.

Once the old peer is removed you can add the new peer to the consensus set with
`flynn-host promote $PEER_IP`. This may take a short time as the new peer
replicates data from the Raft leader before starting to service operations.

When the process is complete a `discoverd` deployment should be run to update
the `DISCOVERD_PEERS` environment variable. You can retrieve the current value
with `flynn -a discoverd env:get DISCOVERD_PEERS`. Replace the address of the
old peer with the new one and update the value with `flynn -a discoverd env:set
DISCOVERD_PEERS=$PEER_IPS`

At this point the host has been replaced successfully and the cluster will
regain full fault tolerance.

## Controller Keys

The Flynn CLI requires a controller authentication key to interact with
a cluster. The key is generated at cluster creation time, but it can be changed
and new keys can be added. Keys are stored in the environment variable
`AUTH_KEY`, a comma-separated list of authentication keys.

    # Generate a new random key
    NEW_KEY=$(openssl rand -hex 16)

    # Add the new key alongside the existing key
    flynn -a controller env:set -t web AUTH_KEY=$NEW_KEY,$(flynn -a controller env:get AUTH_KEY)

To rotate an authentication key:

    # Generate a new random key
    NEW_KEY=$(openssl rand -hex 16)

    # Add both the old and new keys to just the web process type
    flynn -a controller env:set -t web AUTH_KEY=$NEW_KEY,$(flynn -a controller env:get AUTH_KEY)

    # Update internal apps to use the new key
    flynn -a gitreceive env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a tarreceive env:set CONTROLLER_KEY=$NEW_KEY AUTH_KEY=$NEW_KEY
    flynn -a blobstore env:set AUTH_KEY=$NEW_KEY
    flynn -a taffy env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a redis env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a mariadb env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a mongodb env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a kafka env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a clickhouse env:set CONTROLLER_KEY=$NEW_KEY

    # Set the global key to be the new key
    flynn -a controller env:set AUTH_KEY=$NEW_KEY

    # Unset the web process-specific key
    flynn -a controller env:unset -t web AUTH_KEY

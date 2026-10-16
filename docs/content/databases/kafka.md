---
title: Kafka
layout: docs
---

# Kafka

Kafka is a Flynn **plugin** (not part of the bootstrap tarball). Install it on a
cluster host, then provision from an app. See [Plugins](../plugins.md).

```text
sudo flynn-host plugin:install kafka --ref vX
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-kafka.git --ref vX
sudo flynn-host plugin:install ../flynn-plugin-kafka
flynn resource:add kafka
```

The controller app is `kafka-plugin` (`flynn-host ps`, `flynn -a kafka-plugin`).
Provider name stays `kafka`.

The plugin provisions an [Apache Kafka](https://kafka.apache.org) cluster that
runs in [KRaft mode](https://kafka.apache.org/documentation/#kraft) (no
ZooKeeper). A cluster is spread across the nodes of your Flynn install. The
KRaft controller quorum has one voter below three brokers and three voters from
three brokers on; the other brokers are observers. With two brokers, losing the
single voter stops metadata changes until it returns.

The image installs Kafka 3.9.0 (Scala 2.13) from
`img/kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz`, vendored in the plugin repo
in split parts and checked against a SHA-512 sum.

Topics are **not** created automatically. Auto topic creation is disabled on the
brokers, so a topic must be created with the `flynn kafka` CLI before any
producer or consumer can use it. Topic and group names must stay under the
resource prefix.

Each resource gets its own SCRAM-SHA-256 user and ACLs limited to
`flynn.<resource-id>.`. The client listener requires SASL. The broker admin
principal is not returned to the app. The provider sets `tenant_safe`, so
hosted tenants may provision it. See [Plugins](../plugins.md).

## Usage

### Adding a cluster to an app

Kafka is available after the operator installs the plugin. After you create an
app, provision a cluster with:

```text
flynn resource:add kafka
```

This provisions a Kafka cluster as a Flynn app and configures your application
to connect to it.

### Scaling the cluster

Kafka has no followers, joined resources, or MirrorMaker copies. Each resource
is one cluster, and `flynn resource:add kafka --follow` is rejected. Change the
number of brokers with:

```text
flynn kafka:scale <n> [<resource>]
```

`<n>` is 1 to 9. Scaling down drains each removed broker first: its
partitions are reassigned to the remaining brokers and the broker is
unregistered before it stops. A scale finishes with a one-at-a-time broker
roll; each broker keeps its volume and node id. A broker rejoins automatically
when its Flynn host returns or its job is rescheduled. The dashboard records
topology changes as events `P19` (node added), `P20` (node removed), `P21`
(node rejoined), and `P22` (active controller changed).

### Connecting to the cluster

Provisioning adds several environment variables to your app release. Every
provision and attach also sets `FLYNN_KAFKA_<COLOR>_URL` unless `--as NAME` names
the attachment (`NAME_URL`, or `FLYNN_KAFKA_<COLOR>_URL` when `--as` is a color
short name such as `AMBER`). A new provision also sets `KAFKA_URL` when that key
is free; attaching an existing resource does not.

* `KAFKA_URL` / `KAFKA_BROKER_URLS` — connection URL(s) for the cluster. The
  scheme is `kafka+ssl://` when TLS is enabled and `kafka://` otherwise.
* `FLYNN_KAFKA_BOOTSTRAP_SERVERS` — `host:port` bootstrap list for Kafka clients.
* `KAFKA_HOST`, `KAFKA_PORT` — the discoverd host and client port.

New provisions use `kafka+ssl://leader.<cluster>.discoverd:9092` for
`KAFKA_URL`, `KAFKA_BROKER_URLS`, and `FLYNN_KAFKA_BOOTSTRAP_SERVERS`, and
`leader.<cluster>.discoverd` for `KAFKA_HOST`. User jobs cannot resolve the
bare `<cluster>.discoverd` name (discoverd returns NXDOMAIN). One leader is
enough to bootstrap; clients learn the other brokers from metadata. The broker
certificate covers `leader.<cluster>.discoverd`.

The app release also receives `FLYNN_KAFKA_TLS_ENABLED`, `FLYNN_KAFKA_TRUSTED_CERT`,
`FLYNN_KAFKA_CLIENT_CERT`, `FLYNN_KAFKA_CLIENT_CERT_KEY`, `FLYNN_KAFKA_SASL_MECHANISM`,
`FLYNN_KAFKA_SASL_USERNAME`, `FLYNN_KAFKA_SASL_PASSWORD`, and `FLYNN_KAFKA_TOPIC_PREFIX`. The
CLIENT listener is mutual TLS plus SCRAM, so a URL alone cannot connect.
Topics and consumer groups must stay under `FLYNN_KAFKA_TOPIC_PREFIX`.

When TLS is enabled (the default), the following are also injected so your app
can authenticate with mutual TLS:

* `FLYNN_KAFKA_TRUSTED_CERT` — the cluster CA certificate (PEM). Use this as your
  client's trust store.
* `FLYNN_KAFKA_CLIENT_CERT`, `FLYNN_KAFKA_CLIENT_CERT_KEY` — the client certificate and
  private key (PEM) presented to the brokers.
* `FLYNN_KAFKA_TLS_ENABLED` — set to `true`.

Because brokers advertise dynamic per-node IP addresses, clients validate the
broker certificate against the cluster CA rather than by hostname. Set
`ssl.endpoint.identification.algorithm` to an empty string in your client
configuration.

## Transport security (TLS)

TLS is handled with a private certificate authority that is generated when the
cluster is provisioned — there is no dependency on public DNS or Let's Encrypt,
which cannot validate the internal `.discoverd` addresses the cluster uses.

Kafka is configured with three listeners:

| Traffic | Listener | Protocol |
| --- | --- | --- |
| KRaft controller quorum | `CONTROLLER` (9093) | PLAINTEXT (internal only) |
| Broker ↔ broker | `INTERNAL` (9094) | PLAINTEXT (internal only) |
| Application / external clients | `CLIENT` (9092) | SSL (default) or PLAINTEXT |

Only the client-facing `CLIENT` listener uses TLS. Inter-broker and controller
traffic stays on the private cluster network in plaintext, which keeps the
quorum robust and avoids per-broker certificate distribution.

TLS is enabled by default. To disable it cluster-wide, set
`KAFKA_TLS_ENABLED=false` on the `kafka-plugin` system app before provisioning:

```text
flynn -a kafka-plugin env:set KAFKA_TLS_ENABLED=false
```

## Managing topics

Topics must be created before they can be used.

```text
# List topics
flynn kafka:topics

# Create a topic with 12 partitions, replication factor 3 and 7 days retention
flynn kafka:topics:create events --partitions 12 --replication 3 --retention 7d

# Create a compacted topic with an arbitrary Kafka topic config
flynn kafka:topics:create audit --config cleanup.policy=compact --config max.message.bytes=2000000

# Describe a topic (partitions, replicas, in-sync replicas and config)
flynn kafka:topics:info events

# Change a topic's configuration
flynn kafka:topics:configure events --retention 30d --config cleanup.policy=compact

# Delete a topic and all of its data
flynn kafka:topics:destroy events
```

The `--retention` flag accepts a duration such as `168h` or `7d` and is
translated into the `retention.ms` topic config. Any Kafka topic setting can be
supplied with one or more `--config key=value` flags.

## Managing consumer groups

```text
# List consumer groups
flynn kafka:consumer-groups

# Register a consumer group against a topic (seeds earliest offsets by default)
flynn kafka:consumer-groups:create workers events

# Seed at the latest offset, a specific offset, a timestamp, or a relative shift
flynn kafka:consumer-groups:create workers events --to-latest
flynn kafka:consumer-groups:create workers events --to-offset 12
flynn kafka:consumer-groups:create workers events --to-datetime 2024-01-01T00:00:00.000
flynn kafka:consumer-groups:create workers events --shift-by -1

# Describe a group's offsets and lag
flynn kafka:consumer-groups:info workers

# Delete a group
flynn kafka:consumer-groups:destroy workers
```

Kafka has no explicit "create group" operation; `kafka:consumer-groups:create` seeds
committed offsets for the topic's partitions to register the group. The default is
`--to-earliest`. Pass `--to-latest`, `--to-offset`, `--to-datetime`, or `--shift-by`
to choose a different reset, the same flags as `kafka-consumer-groups.sh`.

All `flynn kafka` commands run inside a container on the Flynn cluster, so they
require no local Kafka installation and no firewall or security changes. When
TLS is enabled they transparently use the cluster's client certificate.

### External access

Export Kafka on a TCP route with a stable hostname, then open the host port:

```text
flynn resource:expose kafka
sudo flynn-host firewall:expose PORT   # on every host
```

Keep **passthrough** (the default): brokers already speak TLS on the client
listener, and clients must present `FLYNN_KAFKA_CLIENT_CERT` /
`FLYNN_KAFKA_CLIENT_CERT_KEY` and trust `FLYNN_KAFKA_TRUSTED_CERT`. Router terminate would
break that mutual TLS.

You can still create the route yourself:

```text
flynn -a $(flynn env:get FLYNN_KAFKA) route:add tcp --service $(flynn env:get FLYNN_KAFKA) --leader --domain kafka.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove with `flynn resource:unexpose kafka` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Metrics

Broker releases set `FLYNN_METRICS_PORT=9180`. Each broker serves
`GET /metrics/log` on that port with these `sample#` series:
`service_available`, `errors`, `controller_leader`, `brokers`, `live_brokers`,
`voters`, `under_replicated_partitions`, and `offline_partitions`. An
unreachable broker reports `service_available=0 errors=1`. Dashboard samples
are posted once per UTC minute per resource per attached app. See
[Plugins — Resource metrics line](../plugins.md#resource-metrics-line).

## Upgrades

A plugin update rolls the cluster one broker at a time. Each step waits until
all brokers are live with zero under-replicated partitions. The first plugin
start after this update rolls every existing cluster once.

## Dashboard

The Kafka data source page has the tabs Overview (attached apps and Brokers),
Metrics, Topics, Groups, and Backup. There is no followers or MirrorMaker page.

## Safety

The Kafka appliance stores its log segments on a persistent volume attached to
each broker. Replication (with `min.insync.replicas`) provides durability across
brokers in a multi-broker cluster. A one-broker cluster runs with a replication
factor of one and provides no availability or durability guarantees; treat
that configuration as suitable for development and testing only.

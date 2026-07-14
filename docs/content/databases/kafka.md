---
title: Kafka
layout: docs
---

# Kafka

The Flynn Kafka appliance provisions an [Apache Kafka](https://kafka.apache.org)
cluster that runs in [KRaft mode](https://kafka.apache.org/documentation/#kraft)
(no ZooKeeper). A cluster is spread across the nodes of your Flynn install, with
each broker acting as both a broker and a KRaft controller so the quorum forms
automatically.

Topics are **not** created automatically. Auto topic creation is disabled on the
brokers, so a topic must be created with the `flynn kafka` CLI before any
producer or consumer can use it.

## Usage

### Adding a cluster to an app

Kafka comes ready to go as soon as you've installed Flynn. After you create an
app, provision a cluster for it by running:

```text
flynn resource add kafka
```

This provisions a Kafka cluster as a Flynn app (three brokers, or a single
broker on a single-node/`SINGLETON` install) and configures your application to
connect to it.

### Connecting to the cluster

Provisioning adds several environment variables to your app release:

* `KAFKA_URL` / `KAFKA_BROKER_URLS` — connection URL(s) for the cluster. The
  scheme is `kafka+ssl://` when TLS is enabled and `kafka://` otherwise.
* `KAFKA_BOOTSTRAP_SERVERS` — `host:port` bootstrap list for Kafka clients.
* `KAFKA_HOST`, `KAFKA_PORT` — the discoverd host and client port.

When TLS is enabled (the default), the following are also injected so your app
can authenticate with mutual TLS:

* `KAFKA_TRUSTED_CERT` — the cluster CA certificate (PEM). Use this as your
  client's trust store.
* `KAFKA_CLIENT_CERT`, `KAFKA_CLIENT_CERT_KEY` — the client certificate and
  private key (PEM) presented to the brokers.
* `KAFKA_TLS_ENABLED` — set to `true`.

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
`KAFKA_TLS_ENABLED=false` on the `kafka` system app before provisioning:

```text
flynn -a kafka env set KAFKA_TLS_ENABLED=false
```

## Managing topics

Topics must be created before they can be used.

```text
# List topics
flynn kafka topics

# Create a topic with 12 partitions, replication factor 3 and 7 days retention
flynn kafka topics create events --partitions 12 --replication 3 --retention 7d

# Create a compacted topic with an arbitrary Kafka topic config
flynn kafka topics create audit --config cleanup.policy=compact --config max.message.bytes=2000000

# Describe a topic (partitions, replicas, in-sync replicas and config)
flynn kafka topics info events

# Change a topic's configuration
flynn kafka topics configure events --retention 30d

# Delete a topic and all of its data
flynn kafka topics destroy events
```

The `--retention` flag accepts a duration such as `168h` or `7d` and is
translated into the `retention.ms` topic config. Any Kafka topic setting can be
supplied with one or more `--config key=value` flags.

## Managing consumer groups

```text
# List consumer groups
flynn kafka consumer-groups

# Register a consumer group against a topic
flynn kafka consumer-groups create workers events

# Describe a group's offsets and lag
flynn kafka consumer-groups info workers

# Delete a group
flynn kafka consumer-groups destroy workers
```

Kafka has no explicit "create group" operation; `consumer-groups create` seeds
the earliest committed offsets for the topic's partitions to register the group.

All `flynn kafka` commands run inside a container on the Flynn cluster, so they
require no local Kafka installation and no firewall or security changes. When
TLS is enabled they transparently use the cluster's client certificate.

### External access

An external route can be created to allow access from services not running on
Flynn:

```text
flynn -a $(flynn env get FLYNN_KAFKA) route add tcp --service $(flynn env get FLYNN_KAFKA) --leader
```

External clients must present the client certificate (`KAFKA_CLIENT_CERT` /
`KAFKA_CLIENT_CERT_KEY`) and trust the cluster CA (`KAFKA_TRUSTED_CERT`).

For security reasons this port should be firewalled and only accessed over the
local network, VPN, or SSH tunnel.

## Safety

The Kafka appliance stores its log segments on a persistent volume attached to
each broker. Replication (with `min.insync.replicas`) provides durability across
brokers on multi-node installs. On single-node/`SINGLETON` installs the cluster
runs a single broker with a replication factor of one and provides no
availability or durability guarantees; treat that configuration as suitable for
development and testing only.

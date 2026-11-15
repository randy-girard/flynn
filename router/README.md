# Router

[![](https://f.cloud.github.com/assets/13026/2060788/42916822-8c30-11e3-8c0d-ae743b905759.jpg)](https://commons.wikimedia.org/wiki/File:HebdrehwaehlerbatterieOrtsvermittlung_4954.jpg)

Router is the Flynn HTTP/TCP cluster router. It relies on [service
discovery](../discoverd) to keep track of what backends are up and acts as
a standard reverse proxy with random load balancing. HTTP domains and TCP ports
are provisioned via a HTTP API. Only two pieces of data are required: the domain
name and the service name. PostgreSQL is used as a pluggable persistence backend
so that all instances of router get the same configuration.

Each router process caps in-flight HTTP requests at 512 per app
(`parent_ref`, which the controller sets to `controller/apps/<app id>`).
Every HTTP route of that app shares the cap, including routes to different
services. The cap is not multiplied by host count: a 3-host cluster enforces
512 on each router. At the cap the router returns a plain-text 503
(`App HTTP concurrency limit exceeded`, `Retry-After: 1`) and does not dial
a backend or queue. Streams and upgraded connections hold a slot until they
finish. Idle keep-alives, TCP accepts, and file descriptors are not capped.
A rejection is logged as `http concurrency limit` and is not a backend latency
sample. See [Apps — Routes](../docs/content/apps.md#routes).

TCP routes may store a hostname and a TLS mode:

* `tls_mode=""` — plaintext proxy
* `tls_mode="passthrough"` — backend speaks TLS (datastore export default)
* `tls_mode="terminate"` — router wraps the listener (certificate or `--auto-tls`)

`flynn resource:expose` creates a TCP route in this range (ports 3000–3500).
`flynn-host firewall:expose` opens the host port.

### Benefits over HAProxy/nginx

The primary benefits are that it uses service discovery natively and supports
dynamic configuration. Both HAProxy and nginx require a new process to be
spawned to change the majority of their configuration.

# Router

[![](https://f.cloud.github.com/assets/13026/2060788/42916822-8c30-11e3-8c0d-ae743b905759.jpg)](https://commons.wikimedia.org/wiki/File:HebdrehwaehlerbatterieOrtsvermittlung_4954.jpg)

Router is the Flynn HTTP/TCP cluster router. It relies on [service
discovery](../discoverd) to keep track of what backends are up and acts as
a standard reverse proxy with random load balancing. HTTP domains and TCP ports
are provisioned via a HTTP API. Only two pieces of data are required: the domain
name and the service name. PostgreSQL is used as a pluggable persistence backend
so that all instances of router get the same configuration.

The router's own API (`router-api`, port 5000 on the host network: `/events`,
`/metrics`, `/debug`) requires the cluster key (`CONTROLLER_KEY` or an
`AUTH_KEY` entry) as HTTP Basic password or Bearer token. Only
`/.well-known/status` is public. `router/client` sends the key from its
environment.

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

When no backend can serve a request the router returns its built-in 503
page. Set `ERROR_503_PAGE_URL` on the router app to serve your own page
instead. The router fetches it once, in the background, after its listeners
are up, so a slow or unreachable page host never delays ingress. Until the
fetch finishes the built-in page is served. The fetch has 10 seconds in all
(connect, headers and body, up to 1 MB). If it fails, times out, or returns a
status other than 200, the router logs `using the built-in page` and keeps
it; on success it logs `using the custom 503 page`. The page is not fetched
again while the router runs: `flynn -a router env:set ERROR_503_PAGE_URL=...`
deploys a new router release, one host at a time, and each new router
fetches the page when it starts.

TCP routes may store a hostname and a TLS mode:

* `tls_mode=""` — plaintext proxy
* `tls_mode="passthrough"` — backend speaks TLS (datastore export default)
* `tls_mode="terminate"` — router wraps the listener (certificate or `--auto-tls`)

The router pre-binds 3000–3500. New TCP routes are allocated from 10000–29999
and listen on demand. `flynn addons:expose` uses that window. The host firewall
opens the allocated port, not the whole window. Existing routes in 3000–3500
keep working on the pre-bound listeners.

Updating a TCP route (new certificate, service, or `leader` flag) and the full
resync after the controller event stream drops reuse the route's existing
socket. Each route has exactly one listener; the old route stops accepting and
releases its service as soon as the new one serves, and a deleted route's port
stops accepting (ports in 3000–3500 go back to the pre-bound pool).

### Benefits over HAProxy/nginx

The primary benefits are that it uses service discovery natively and supports
dynamic configuration. Both HAProxy and nginx require a new process to be
spawned to change the majority of their configuration.

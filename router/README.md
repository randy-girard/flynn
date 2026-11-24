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

The router gets its routes from the controller. For each of HTTP and TCP it
opens the controller's route event stream, lists every route, sets the listed
ones and removes those it has that are not listed (a route deleted before the
list). Events that arrive during the list are folded into it, and later
events are applied as they come. Applying an event twice, or removing a route
the router does not have, changes nothing. When the event stream drops, the
sync ends with `router: sync error` and runs again from the start after
2 seconds: open the stream, list, remove. The stream is never resumed, so a
change made while it was down is not lost, even when the stream had not seen
an event yet. When the controller's node dies without closing the connection
(a reboot), TCP keepalive on the router's controller connections (5 s idle,
3 probes 5 s apart) ends the stream within about 20 seconds.

TCP routes may store a hostname and a TLS mode:

* `tls_mode=""` — plaintext proxy
* `tls_mode="passthrough"` — backend speaks TLS (datastore export default)
* `tls_mode="terminate"` — router wraps the listener (certificate or `--auto-tls`)

The router pre-binds 3000–3500. New TCP routes are allocated from 10000–29999
and listen on demand. `flynn addons:expose` uses that window. The host firewall
opens the allocated port, not the whole window. Existing routes in 3000–3500
keep working on the pre-bound sockets. A pre-bound socket is bound but does
not listen until a route takes it, so a port in 3000–3500 without a route
refuses connections instead of accepting them for nobody.

Each port has at most one listening socket, and exactly one route accepts on
it. Updating a TCP route (new certificate, service, or `leader` flag) and the
full resync after the controller event stream drops reuse the route's existing
socket. The same holds when the route id on a port changes: a route deleted
and re-created on its port while the event stream was down, or a new route's
set seen before the old one's remove. The new route takes over the socket, the
old route stops accepting and releases its service as soon as the new one
serves, and the old id's remove is then a no-op. A deleted route's port is
refused from then on: a port in 10000–29999 is closed, and a port in
3000–3500 goes back to the pre-bound pool as a socket that does not listen.

Updating an HTTP route (an edit, a certificate renewal, or the full resync)
replaces it in place. The router drops the old route's service reference, so
a service no route uses any more stops its discoverd watch. It closes the
old route's idle backend connections. A request still running on the old
route finishes, and its connection is closed after 90 seconds idle. Changing
the domain stops serving the old domain. A domain stays served while any
of its routes remains: removing or moving the root (`/`) route leaves the
path routes reachable, and `/` returns 404 until a root route is set. Once the last route of an app is
gone and its requests have finished, the app's concurrency counter is
deleted too.

### Timeouts

Backend connections notice a backend whose host vanished without a RST (a
power-off or network loss) in bounded time. An idle pooled connection is
dropped by TCP keepalive (5 s idle, 3 probes 5 s apart, about 20 s). A request
without a body fails when what it sent stays unacknowledged for 5 s
(`TCP_USER_TIMEOUT`); a `GET`, `HEAD`, `OPTIONS` or `TRACE` that fails on a
pooled connection before any response byte is then tried on another backend.
While a request body, a TCP route or an upgraded (WebSocket) connection is in
flight the limit is 2 minutes instead. Linux counts the time a backend's
receive window stays closed against this limit even when the backend answers
every probe, so it is also how long a live backend may stop reading: an upload
to tarreceive waiting for a slot, a blobstore PUT waiting on its store, or a
`git push` while git indexes the pack is not cut off. A backend that reads
nothing for longer, or vanishes mid-upload, gets a plain 503 after about
2 minutes (a request with a body is never sent to another backend). The
router's controller connections use the same keepalive and no user timeout
(described above). A backend has 10 minutes to send its response headers. On the
client side the router waits up to 1 minute for request headers and keeps an
idle keep-alive connection for 5 minutes; it sets no user timeout or write
timeout there, so a client that downloads slowly is not cut off.

### Benefits over HAProxy/nginx

The primary benefits are that it uses service discovery natively and supports
dynamic configuration. Both HAProxy and nginx require a new process to be
spawned to change the majority of their configuration.

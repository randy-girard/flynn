# controller

This is the Flynn Controller. It is loosely inspired by the [Heroku Platform
API](https://devcenter.heroku.com/articles/platform-api-reference) and enables
management of applications running on Flynn via an HTTP API.

The controller depends on the **platform** PostgreSQL appliance (system app
`postgres`) and is typically booted by [bootstrap](../bootstrap). Tenant
databases are `flynn resource:add postgres` after
`flynn-host plugin:install postgres`; they do not live on this appliance.

The [CLI](../cli) is the primary API consumer. Command examples live in
[docs/content/cli.md](../docs/content/cli.md). Some HTTP examples are under
[docs/api-examples](../docs/api-examples).

The scheduler keeps pending, blocked, starting, running, and stopping jobs in
memory. Stopped jobs are dropped from that map once their down row is stored,
or on the next job sync when this process does not still need to write one.
That is the same on a hosted cluster and a self-hosted one. The controller
job list is the history; the in-memory map is not. A later start of a process
type that has already run is a scale. Stopping jobs stay in memory so a
volume and a CPU or memory reservation are not released while the job is
still on the host.

The leader loop owns that in-memory state. Listing jobs or volumes, checking
a host, and streaming a new host all happen off the loop; the loop only
applies the result. A slow host blocks work for that host. Placement of other
apps continues. The leader also refreshes each placeable host's CPU and
memory totals about every 10 seconds. That read stays off the loop, one
round at a time. A failed read keeps the last totals. A reserving job,
hosted or self-hosted, skips a host that has never reported them. The
refresh does not reopen the host's job or volume streams. Controller writes are queued separately and retried for one
minute. When that minute ends, the write is dropped. A failed down write
keeps the stopped job in memory. A failed up, pending, or scale write is
retried when the next sync or formation change asks for it again.

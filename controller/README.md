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

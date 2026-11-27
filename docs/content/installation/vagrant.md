---
title: Vagrant
layout: docs
toc_min_level: 2
---

# Vagrant

The repository `Vagrantfile` boots Ubuntu 24.04 (`bento/ubuntu-24.04`) VMs for building Flynn and for multi-node clusters. It replaces the old `flynn-base` demo box (that image is no longer published).

Install [Vagrant](https://www.vagrantup.com/) (≥ 1.9) and [VirtualBox](https://www.virtualbox.org/). The `vagrant-disksize` plugin is used to grow disks.

Clone this repository, then from the repo root:

## Builder VM

The **builder** VM runs `setup.sh` (Go 1.24, Docker, ZFS, PostgreSQL/MariaDB/MongoDB/Redis test packages) and mounts the source tree at `/root/go/src/github.com/flynn/flynn`. It starts Flynn only so `flynn-builder` can compile cluster images. It is not the live development cluster.

```text
vagrant up builder
vagrant ssh builder
```

Inside the VM:

```text
cd /root/go/src/github.com/flynn/flynn
make
```

See [Development](../development.html.md) for build, test, and release details.

The builder is sized for compiling cluster images (default 30 GB RAM / 8 CPUs, overridable with `VAGRANT_MEMORY` and `VAGRANT_CPUS`). Shrink those if you are only iterating on a single component.

## Cluster nodes

`node1` … `nodeN` are cluster members on a host-only network (`192.168.56.20+`). Flannel VXLAN needs promiscuous mode on that NIC (the Vagrantfile sets it).

```text
# default: three nodes
vagrant up node1 node2 node3
```

Set `FLYNN_MAX_NODES` if you need a larger (or singleton) topology.

Reach cluster services on the host-only addresses, not Vagrant NAT port forwards: `http://192.168.56.20/` is node1 HTTP, `.21` is node2, and so on. SSH is still `vagrant ssh` (Vagrant's default NAT forward for guest 22). The private network does not filter ports; Flynn's host firewall does. `22`/`80`/`443` are installer-owned. A live TCP route is opened by the host daemon, one port, not the whole `10000–29999` window. `sudo flynn-host firewall:expose PORT` is only for a port that is not a route.

Provisioning a cluster from these VMs is the same as [manual installation](manual.md): install `flynn-host` on each node, `flynn-host init` with `--peer-ips` or a discovery token, then `flynn-host bootstrap`.

`script/vagrant-smoke.sh` (or `make vagrant-smoke`) is the acceptance suite (`--item quick`, `--item datastores`, and so on). The same entrypoint also has VM lifecycle commands for the smoke env (`.vagrant`, **builder** + **nodeN**). They do not touch the laptop loop:

```text
make vagrant-smoke-status
make vagrant-smoke-ssh               # builder; or: make vagrant-smoke-ssh VM=node1
make vagrant-smoke-up                # boot VMs already in .vagrant
make vagrant-smoke-reload            # alias: restart
make vagrant-smoke-stop              # alias: halt
make vagrant-smoke-destroy           # alias: teardown; ./build stays
# same commands: script/vagrant-smoke.sh status|ssh|up|reload|stop|destroy
```

Scripts live under `script/vagrant/` (shared `lib/`, smoke lifecycle in `smoke-env.sh`). `script/vagrant/suite.sh` is still the suite.

## Laptop development cluster

`script/vagrant.sh` is the laptop loop (the default Vagrant script). Smoke keeps the `-smoke` suffix so both can be up without sharing running VMs. The laptop env uses `.vagrant-dev`, **dev-builder** at `192.168.57.10` (compile only), and **dev-node1** at `192.168.57.20` as the live cluster by default (`FLYNN_DEV_NODES=1`). Set `FLYNN_DEV_NODES=0` for builder-only, or `N` for `dev-node1` … `dev-nodeN` on `192.168.57.(19+N)`. Do not run a bare `vagrant up` for this loop either; that still boots smoke.

On **aarch64** VMs (Apple Silicon), cluster nodes install `qemu-user-static` **9+** during install, reload, and update so x86_64 Heroku buildpack binaries (including Node 24) can run inside slugbuilder. Ubuntu 24.04's qemu 8.2 crashes Node (`QEMU internal SIGSEGV {code=MAPERR, addr=0x20}`). The heroku-24 image on that architecture also installs amd64 glibc. Rebuild images after pulling the glibc change, then `script/vagrant.sh update` (or `bootstrap` on a new cluster). After a qemu-user upgrade, re-register binfmt on every node (`ensure-qemu-binfmt.sh`); `git push` prints a qemu-mode notice on arm64 hosts.

The staticfile buildpack compiles its supply program with Go. On arm64 that compile uses the official linux-arm64 Go 1.23.4 toolchain (`GOTOOLCHAIN=local`). The upstream linux_x64 Go 1.22.5 toolchain fetches another amd64 Go, and qemu-user faults in that runtime's epoll poller. Rebuild slugbuilder after pulling this change. The nginx binary that buildpack installs is still linux-x64. The heroku-24 image installs `libpcre2-8-0:amd64` and `libcrypt1:amd64` so that binary can load under qemu-user. Rebuild slugrunner after pulling those packages.

Laptop `git push` authenticates with `flynn git-credentials`. `cluster:add` records a **native** Flynn CLI (`/usr/local/bin/flynn` after `make vagrant-cli`). Do not point git at `build-dev/bin/flynn`: Vagrant image builds replace that path with a Linux `flynn-linux-*` symlink, which macOS cannot execute.

```text
make vagrant-setup           # first time: boot, build images if needed, bootstrap cluster nodes, connect
make vagrant-up              # boot dev-builder and dev-node1
make vagrant-reload          # rolling node-failure reboot; remaining hosts recover, node rejoins
make vagrant-monitor         # health sample every 30s: Flynn, dashboard, tenant apps, plugin resources
make vagrant-chaos           # inject random faults and fail if the cluster does not recover
make vagrant-stop            # halt VMs (disks stay)
make vagrant-destroy         # delete VMs; ./build-dev stays
make vagrant-status
make vagrant-ssh             # builder; or: make vagrant-ssh VM=dev-node1
make vagrant-build           # boot builder if needed and build images (works before setup)
make vagrant-cli             # build the laptop flynn CLI and install it to /usr/local/bin
make vagrant-bootstrap       # install the tarball and flynn-host bootstrap on cluster nodes
make vagrant-update          # build on the builder if Flynn source changed, then flynn-host update --all-nodes on running cluster nodes
make vagrant-plugin PLUGIN=dashboard  # build ../flynn-plugin-dashboard and install it, or update it if already installed
make vagrant                 # help
# same commands: script/vagrant.sh setup|up|reload|monitor|chaos|stop|destroy|status|ssh|build|cli|bootstrap|update|plugin
```

`monitor` prints one health sample every `MONITOR_INTERVAL` seconds (default 30) until Ctrl-C, or `MONITOR_COUNT` samples (its exit status is the last sample's). Each sample runs `guest/check-health.sh` on the first running cluster node and covers: the cluster probe (hosts, controller, scheduler, every postgres peer, the sirenia membership, every service at its formation count, `status.<domain>`); tenant HTTP; every `*-plugin` service instance answering HTTP; `dashboard.<domain>/.well-known/status`; and a TCP connect to the discoverd leader behind every provider resource URL (platform postgres, redis, postgres, mysql primaries and followers). It never prints credentials or resource env values. The sirenia membership check reads the platform postgres state from the discoverd service meta and fails when a peer is listed twice in the primary, sync and async chain (by `POSTGRES_ID`, or by address), when there is no sync on a multi-peer cluster, or when the chain and the registered postgres instances differ (`sirenia: peer <id> listed twice (sync, async[0])`, `peer <id> is registered but not in the sirenia chain`). A healthy answer from each peer's own status does not show such a chain.

`chaos` waits for a healthy baseline, then injects `CHAOS_ROUNDS` faults (default 10). It shuffles `CHAOS_FAULTS` once and cycles through that order. The default list is every kind:

| Fault | What happens |
|---|---|
| `system-job` | `flynn-host stop` a random Layer 1 job (controller, router, discoverd, flannel, status, blobstore, …) |
| `plugin-job` | stop a random `*-plugin` job (dashboard, datastore APIs, …) |
| `datastore-primary` | stop the leader job of a random provider resource, forcing a failover |
| `datastore-replica` | stop a non-leader database peer of a provider resource (an instance of its discoverd service, never another process of the app such as the postgres `web` API job) |
| `tenant-job` | stop a random job of a user app |
| `host-restart` | `systemctl restart flynn-host` on a random node |
| `node-reboot` | `vagrant halt --force` a random node, wait `CHAOS_NODE_DOWN` seconds (default 60), boot it and start `flynn-host` |

After each fault it waits `CHAOS_SETTLE` seconds (default 20), then up to `CHAOS_TIMEOUT` seconds (default 900) for every monitor check to pass. It stops at the first round that does not recover, prints the failing checks, and exits 1. A round can pass the monitor checks while a postgres peer is still resyncing, so before the next fault it also waits for full strength with the gate `reload` and `update` use (hosts, controller, status, every postgres peer healthy, the sirenia membership, every service at its formation count; up to `CHAOS_SETTLE_TRIES` five-second tries, default 120), and fails with `FAILED: cluster did not return to full strength after round <n> <fault>: <reason>` when the cluster does not get there. After the last round the same gate runs once more (also when that round was skipped), and the run fails with `FAILED: cluster did not return to full strength after chaos: <reason>` (the probe's last error, for example `postgres peer 10.0.0.2 is not healthy`) when the cluster is not back by then, so the next command never starts on a degraded cluster. The report shows how long each wait took (`SETTLE` column, `settle` line), and the gate's probe output is in `settle.log`. The gate is not part of a round's budget window. A kind with nothing running (for example no tenant jobs) is skipped, but a run where every round was skipped exits 1 (`FAILED: no fault was injected`), and `CHAOS_MAX_SKIPPED` (unset by default) fails a run with more skipped rounds than that. A round that did recover still fails the run when its recovery took longer than `CHAOS_TIMEOUT` (the last health sample can run past it), or when the background samples taken during it break a budget in `CHAOS_BUDGET`: a list of `<fault>:<check>=<max>`, where `<check>` is a health section (`cluster`, `tenants`, `plugins`, `resources`, `orphans`, `bridge`, `monitor`, `nodes`) and `<max>` the failed samples of it allowed during that round, `unhealthy` counts every failed sample, and `recovery` is the most seconds the round may take. Two more checks come from the ingress prober (below): `ingress` is the most failed probe seconds allowed in that round, and `ingress-longest` the longest run of consecutive failed seconds of one target. The default is `tenant-job:tenants=0 datastore-replica:tenants=0` plus `<fault>:ingress=0` for every fault kind except `node-reboot:ingress=10 node-reboot:ingress-longest=5`: stopping one tenant job or a datastore replica must not cost a single failed tenant ingress sample, and no fault may cost one second in which no node serves a probed target. A powered-off node does not reset its connections, so a router that holds a pooled connection to a backend there waits up to its 5 s dead-peer bound before it retries another backend; the dev cluster showed 0 to 1 such seconds per `node-reboot` round. chaos does not deploy a tenant app; it uses the apps that have jobs (and, for the prober, the ones that answer) at baseline. A `tenant-job` round that stops the only `web` job of an app is a real outage and fails the default budget with `tenant app <app> runs a single web job; scale web=2 or set CHAOS_BUDGET`. Give a tenant app two web jobs before a chaos run, for example `flynn -a <app> scale web=2`, or set `CHAOS_BUDGET` (`CHAOS_BUDGET=""` turns budgets off). A typo fails before the first fault. The job kinds never guess at a target: if no controller instance can list the provider resources, the round fails without stopping a job (datastore jobs could not be told apart from other system or tenant jobs), and a datastore whose discoverd leader or instances lookup fails is left out of both `datastore-primary` and `datastore-replica`; a round with nothing left to pick for that reason fails instead of being skipped. Set `CHAOS_APP` (for example `kafka`) to limit the `datastore-primary` and `datastore-replica` kinds to resource apps whose name contains it. The run prints its `CHAOS_SEED`; set it to repeat the same order and targets, e.g. `make vagrant-chaos CHAOS_ROUNDS=3 CHAOS_FAULTS="datastore-primary node-reboot" CHAOS_SEED=42`.

You do not need to run `monitor` alongside `chaos`. After the baseline passes, `chaos` runs the same health sample in the background every `CHAOS_MONITOR_INTERVAL` seconds (default 15) until the last round ends, including while a fault is being injected. During a `host-restart` or `node-reboot` round it samples from another node until the round recovers, because the faulted node's own discoverd and router are down; the recovery check itself still runs from the first running node. A sample whose check could not run (for example the node was unreachable) is recorded as `monitor: health check on <node> exited <status> without a result`. At the end it prints a report and writes it to `./flynn-logs/chaos/<UTC time>/report.txt` (`CHAOS_REPORT_DIR` sets the directory). The report has the verdict and seed, one row per round (result, recovery time, which checks failed during that round, target), a per-check table (failed samples, number of outages, longest outage, first failure), and each distinct failure message with how many samples showed it. The same directory has `report.html`, a self-contained page with the same tables, summary cards, and a health timeline with one cell per sample for each check (hover a cell for its time and failure messages). The raw data is next to it: `samples.tsv` (one line per health sample) and `rounds.tsv` (one line per fault), and `ingress.tsv` (one line per ingress probe).

A health sample takes several seconds and runs every 15, so a short ingress outage between two samples is not seen by it. For the whole run (from the baseline to the final gate) chaos also runs `script/vagrant/host/ingress-probe.sh` on the laptop: once a second it sends one request per target to every cluster node IP (`CHAOS_INGRESS_NODES`, default every dev node: the same list the health check uses), with a `CHAOS_INGRESS_TIMEOUT` (default 1.5) second timeout, and appends `epoch<TAB>node<TAB>target<TAB>status<TAB>latency_ms` to `ingress.tsv`. The targets are `router` (the router itself: `http://chaos-ingress-probe.<domain>/` must answer 404), `controller` (`https://controller.<domain>/ping` must answer 200) and `app:<name>` for each tenant app that answered at baseline (200-499). A probe belongs to a round when its time is between the fault and the round's recovery (the same window `rounds.tsv` records; the settle gate is not part of it). A second counts as failed for a target only when no node answered it as expected, because a client can use any node: a `node-reboot` or `host-restart` of one node shows failed probes against that node's IP only. Those per-node failures are in the report but are not part of the verdict. The report's Ingress section (report.txt and report.html) gives the probe count, the failed probe seconds and the longest outage for the whole run and per round, and the failed probes per node. To see what one node alone would give (a single-homed client), set `CHAOS_INGRESS_NODES=dev-node2`; `CHAOS_INGRESS=0` turns the prober off. The prober stops with the run, also on Ctrl-C.

A tenant app that was deleted while a host still listed its job no longer fails the baseline: when every running node's router answers its hostname with 404 (no route), the health sample drops it at once. A `vagrant status` that fails once right after a node boots (VirtualBox busy, the machine lock) is asked again up to `VAGRANT_STATUS_TRIES` times (default 4, 3 s apart) before a command on that node fails with `<node> is not running (unknown)` and vagrant's own error.

`reload` / `restart` reboot one VM at a time (`vagrant reload --no-provision`) for every machine already in `.vagrant-dev` (or the names you pass, e.g. `make vagrant-reload VM=dev-node1`). They do not create missing VMs. A reboot is treated as a node failure: jobs are not drained first. Remaining hosts (and cluster-monitor) recover scheduler/controller/tenant HTTP; when the VM is back, `flynn-host.service` starts and the node rejoins via `--peer-ips`. Before the first cluster node goes down, the cluster must already pass the check each reboot waits for (hosts, controller, status, postgres peers caught up, every service at its formation count; up to `RELOAD_PREFLIGHT_TRIES` five-second tries, default 60). That check, here and after each reboot and `update`, must pass `STABLE_HOLD_PASSES` times in a row (default 3), because a postgres sync about to reseed answers healthy once before it goes down, and the tenant app list must be readable. Otherwise reload exits non-zero without rebooting a cluster node, so it cannot take down the only good copy of a datastore. The gate is skipped when no cluster node is running or `dev-node1` is not bootstrapped; set `FLYNN_VAGRANT_RELOAD_SKIP_PREFLIGHT=1` to reboot a cluster that is already broken. Tenant HTTP is waited for after Flynn is stable, for the tenant apps that had a job listening on a port before the first reboot (a worker-only app has no route to answer and is not waited for). An app in that list that no longer has a route (deleted during the reload: HTTPS fails the TLS handshake and HTTP gets the router 404 for `GONE_TRIES` rounds in a row, default 6) is dropped from the wait instead of failing it. A single-node cluster is down until that VM is back. Cluster nodes use `flynn-host.service` (from `install-flynn`); after a reboot the script starts that unit if the cluster was already bootstrapped. The builder is not started as an operator cluster.

`setup` bootstraps from the layer cache. If there is no tarball and the cache is empty, it builds images on **dev-builder** instead of failing with “run build then setup again.” `build` can run first on a fresh builder (it boots `dev-builder` if needed). Builder Flynn is only the compile toolchain (`build.sh` start-all / stop-all around flynn-builder). The live cluster is `install-flynn` + `flynn-host init --peer-ips` + `flynn-host bootstrap` on **dev-nodeN**, the same path smoke tests. `update` rebuilds cluster images on the builder when Flynn source (not docs) is newer than the last tarball, then runs `flynn-host update --all-nodes --tarball --force` on the running cluster nodes so you exercise a real rolling update. It returns once the cluster is back at full strength (postgres peers caught up and every service at its formation count, up to `UPDATE_STABLE_TRIES` five-second tries, default 180) and the full probe passes, so a following `reload` or `chaos` starts from a healthy cluster. The short steps it runs on every node after `flynn-host update` (node DNS, `FLYNN_ROOT`, qemu binfmt) and every cluster probe go over `vagrant ssh` with a deadline (`FLYNN_VAGRANT_SSH_TIMEOUT`, default 300 s; probes `FLYNN_VAGRANT_PROBE_TIMEOUT`, default 600 s). VirtualBox NAT port forwards can wedge under load or after a snapshot restore, so ssh exits 255 or hangs; such a call is retried up to `FLYNN_VAGRANT_SSH_TRIES` times (default 3) after toggling that VM's NAT link (`VBoxManage controlvm <id> setlinkstate1 off`/`on`). A step that fails by itself is not retried. When a post-update step still fails, `update` exits non-zero with `the cluster update completed (flynn-host update succeeded), but post-update step <step> on <vm> failed`: the cluster was updated and re-running `update` is safe. Laptop `cluster:add` talks to **dev-node1** (`192.168.57.20`); it reads the controller job `AUTH_KEY` (`flynn-host cli-add-command`), not a stale `host.json` secret.

`stop` / `halt` run `vagrant halt`. `destroy` / `teardown` run `vagrant destroy -f`. Both stay in `.vagrant-dev` and do not touch smoke. Destroy does not delete `./build-dev` or `./flynn-logs`.

The source tree and `ubuntu_ports_cache` are shared, but Flynn **build outputs** and running VMs are not. Inside the VM they still look like `build/`; on the laptop they land in `./build-dev` (binaries, `images.json`, release tarballs). Smoke keeps `./build`. After this mount is added, run `make vagrant-reload` so the overlay attaches. If you already built on the shared `./build`, copy what you need (`cp -a build/. build-dev/`) or run `make vagrant-build` again.

If an older laptop loop already bootstrapped Flynn on **dev-builder**, run `make vagrant-bootstrap YES=1` (or `setup`) so the operator cluster moves onto **dev-node1**. Do not keep using the builder as `flynn -c local`.

Non-interactive (no sudo password prompt): `make vagrant-setup YES=1`, `make vagrant-update YES=1 FORCE_BUILD=1`, `make vagrant-destroy YES=1`. That passes `--yes` so `sudo -n` is used; if this laptop cannot write `/etc/hosts` without a password, setup still bootstraps the VMs and `make vagrant-probe` checks the cluster from **dev-node1**. Three-node cluster: `make vagrant-setup YES=1 NODES=3`.

`make vagrant-plugin PLUGIN=<name>` (`script/vagrant.sh plugin <name>`) takes `dashboard`, `flynn-plugin-dashboard`, or a path to the checkout. On the first running cluster node it reads `app.name` from that checkout's `flynn-plugin.json` and checks `flynn-host plugin:list`: an installed app gets `plugin:update --rebuild`, otherwise `plugin:install --rebuild`. Either way dist/ is rebuilt, so local source changes ship. `YES=1` also passes `--yes`, which third-party plugins need because `vagrant ssh` has no TTY. Checkouts are synced to `/opt/flynn-plugins/<checkout>` when the VMs boot; after cloning a new one, run `make vagrant-reload`.

`flynn-host plugin:install` / `plugin:update --rebuild` for a sibling checkout compiles on the cluster node (not the builder). `setup` installs the same pinned Go as the builder at `/usr/local/go` and sets `FLYNN_ROOT=/root/go/src/github.com/flynn/flynn` in `/etc/profile.d/flynn-root.sh`, `/etc/environment`, and `/etc/flynn/source-root` so plugin-build finds this Flynn even when `flynn-host` is `/usr/bin`. On an already-running node: `sudo bash script/vagrant/guest/ensure-go.sh` (or `sudo bash script/vagrant/guest/ensure-flynn-root.sh` if Go is already installed).

See [Development](../development.html.md).

## Demo directory

`demo/Vagrantfile` still references the historical `flynn-base` box from `dl.flynn.io`. Do not use it. Use the Vagrantfile at the repository root.

# flynn-test

flynn-test contains full-stack acceptance tests for Flynn.

## Usage

### Bootstrap Flynn

The tests need a running Flynn cluster, so you will need to boot one first.

To run Flynn locally, boot the builder VM:

```text
vagrant up builder
vagrant ssh builder
```

then build and bootstrap Flynn (this may take a few minutes). Integration
tests need the host `flynn-test` binary:

```text
FLYNN_BUILD_TEST_BINARIES=1 make
script/bootstrap-flynn
```

### Run the tests

Run the `flynn cluster:add` command from the bootstrap output to add the cluster to your `~/.flynnrc` file, then run the tests:

```text
flynn cluster:add ...
# from the repository root; script/build-flynn puts both binaries in build/bin
build/bin/flynn-test --flynnrc ~/.flynnrc --cli `pwd`/build/bin/flynn
```

## Auto booting clusters

The test binary is capable of booting its own cluster to run the tests against, provided you are using a machine capable of running KVM.

### Build root filesystem + kernel

Before running the tests, you need a root filesystem and a Linux kernel capable of building and running Flynn.

To build these into `/tmp/flynn`:

```text
mkdir -p /tmp/flynn
sudo rootfs/build.sh /tmp/flynn
```

You should now have `/tmp/flynn/rootfs.img` and `/tmp/flynn/vmlinuz`.

### Build the tests

```text
go build -o flynn-test
```

### Download Flynn CLI

The tests interact with the VM cluster using the Flynn CLI, so you will need it locally.

Download it into the current directory:

```text
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash -s -- --dir .
# or copy build/bin/flynn from a local build
chmod +x flynn
```

### Run the tests

```text
sudo ./flynn-test \
  --user `whoami` \
  --rootfs /tmp/flynn/rootfs.img \
  --kernel /tmp/flynn/vmlinuz \
  --cli `pwd`/flynn
```

## CI

Pull requests against `main` run the [Unit tests](../.github/workflows/unit-tests.yml)
GitHub Actions workflow (`gofmt`, `bats script/test`, `make test-unit-root-native`).
Cluster acceptance is local: [Development — tests](../docs/content/development.html.md#tests)
and `script/vagrant-upgrade-smoke.sh`.

The rest of this section describes the historical in-cluster CI app (KVM nested
clusters). Prefer GitHub Actions plus Vagrant smoke unless you are maintaining
that runner.

### Bootstrap a Flynn cluster

Follow [manual installation](../docs/content/installation/manual.md) to install and
bootstrap a Flynn cluster. Example:

```
curl -fsSL -o install-flynn https://github.com/randy-girard/flynn/releases/latest/download/install-flynn
sudo bash install-flynn
sudo systemctl start flynn-host
CLUSTER_DOMAIN=ci.example.com flynn-host bootstrap
flynn cluster:add -p <tls-pin> default ci.example.com <controller-key>
```

Create a directory to store CI build images (this should be on a fast disk to
minimise IO wait when building clusters, ideally a large tmpfs):

```
sudo mkdir -p /opt/flynn-test
```

### Create the CI app

In your dev environment, build Flynn:

```
make
```

run the CI setup script:

```
test/scripts/setup.sh
```

add the necessary environment variables (assuming the Flynn CI cluster is
configured as `flynn-ci` in your `~/.flynnrc`):

```
flynn -c flynn-ci -a ci env:set AUTH_KEY=xxxxxxxxxx
flynn -c flynn-ci -a ci env:set BLOBSTORE_S3_CONFIG=xxxxxxxxxx
flynn -c flynn-ci -a ci env:set BLOBSTORE_GCS_CONFIG=xxxxxxxxxx
flynn -c flynn-ci -a ci env:set BLOBSTORE_AZURE_CONFIG=xxxxxxxxxx
flynn -c flynn-ci -a ci env:set GITHUB_TOKEN=xxxxxxxxxx
flynn -c flynn-ci -a ci env:set AWS_ACCESS_KEY_ID=xxxxxxxxxx
flynn -c flynn-ci -a ci env:set AWS_SECRET_ACCESS_KEY=xxxxxxxxxx
```

scale up the `runner` process:

```
flynn -c flynn-ci -a ci scale runner=1
```

add a route with the CI TLS key and certificate:

```
flynn -c flynn-ci -a ci route:add http -s ci-web -c <ci.crt> -k <ci.key> ci.example.com
```

CI should now be up and running at `https://ci.example.com`.

### Deploy the CI app

If the CI code has been changed, rebuild Flynn in your dev environment:

```
make
```

Then re-run the CI setup script which will upload the built CI image and deploy
the app:

```
test/scripts/setup.sh
```

If the rootfs needs rebuilding, you will need to scale down the `runner`
process and remove the existing image before deploying and then scaling the
runner back up:

```
flynn -c flynn-ci -a ci scale runner=0
test/scripts/setup.sh
ssh <ci-box> sudo rm -rf /opt/flynn-test/build/{rootfs.img,vmlinuz}
flynn -c flynn-ci -a ci scale runner=1
```

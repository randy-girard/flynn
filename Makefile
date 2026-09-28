GO_ENV=GOROOT=`readlink -f build/_go`

# `make` with no target still builds. `make help` lists every public target.
# Keep descriptions on the same line as the target (`target: ## text`) so help
# stays in sync with the recipes.

build: ## Build host binaries (script/build-flynn)
	script/build-flynn

release: ## Build with a git-derived version stamp
	script/build-flynn --git-version

clean: ## Remove build/ outputs
	script/clean-flynn

# On macOS/Windows, script/run-unit-tests boots a Docker container with the
# Linux test dependencies. On Linux it runs natively (same path as CI).
test: test-unit test-integration ## Run unit tests then integration tests

test-unit: ## Run Go unit tests (Docker on macOS/Windows)
	script/run-unit-tests

test-unit-root: ## Alias for test-unit
	script/run-unit-tests

# Native targets used inside the container / on Linux hosts. Do not call these
# directly from macOS unless you have installed PostgreSQL (and ZFS for volumes).
#
# -gcflags=all=-d=checkptr=0: vendored boltdb does unsafe pointer arithmetic
# that Go's checkptr (enabled with -race) rejects as "converted pointer
# straddles multiple allocations". Pass it as a go test arg (not only via
# GOFLAGS) so sudo invocations cannot drop it.
GO_TEST_CHECKPTR=-gcflags=all=-d=checkptr=0
COVERAGE_DIR ?= coverage
COVERAGE_UNIT = $(COVERAGE_DIR)/unit.out
COVERAGE_VOLUME = $(COVERAGE_DIR)/volume.out

ifeq ($(FLYNN_SKIP_COVERAGE),1)
COVER_UNIT_FLAGS = -cover
COVER_VOLUME_FLAGS = -cover
else
COVER_UNIT_FLAGS = -covermode=atomic -coverprofile=$(COVERAGE_UNIT)
COVER_VOLUME_FLAGS = -covermode=atomic -coverprofile=$(COVERAGE_VOLUME)
endif

test-unit-native: build ## Linux: go test -race (no ZFS volume tests)
	# Exclude host/volume: those need root/ZFS and are run by test-unit-root-native.
	@mkdir -p $(COVERAGE_DIR)
	set +e; \
	env $(GO_ENV) GOFLAGS="-mod=vendor -buildvcs=false" PATH=${PWD}/build/bin:${PATH} \
		go test $(FLYNN_GO_TEST_FLAGS) $(GO_TEST_CHECKPTR) -race $(COVER_UNIT_FLAGS) \
		$$(go list ./... | grep -v '/host/volume'); \
	status=$$?; \
	if [ "$(FLYNN_SKIP_COVERAGE)" != "1" ]; then ./script/report-unit-coverage $(COVERAGE_UNIT); fi; \
	exit $$status

test-unit-root-native: test-unit-native ## Linux: unit tests plus sudo ZFS volume tests
	@echo "==> host/volume tests as root (ZFS)"
	@mkdir -p $(COVERAGE_DIR)
	set +e; \
	sudo -E env $(GO_ENV) GOFLAGS="-mod=vendor -buildvcs=false" PATH=${PWD}/build/bin:${PATH} \
		go test $(FLYNN_GO_TEST_FLAGS) $(GO_TEST_CHECKPTR) -race $(COVER_VOLUME_FLAGS) ./host/volume/...; \
	status=$$?; \
	if [ "$(FLYNN_SKIP_COVERAGE)" != "1" ]; then ./script/report-unit-coverage $(COVERAGE_UNIT) $(COVERAGE_VOLUME); fi; \
	exit $$status

# Requires Docker, ZFS/volumes, and a functional Flynn host (nested containers).
# Set SKIP_INTEGRATION_TESTS=1 (environment or make argument) to skip cluster bootstrap.
test-integration: build ## Boot a nested cluster and run test/ (SKIP_INTEGRATION_TESTS=1 to skip)
ifneq ($(SKIP_INTEGRATION_TESTS),1)
	script/run-integration-tests
else
	@echo >&2 "Skipping integration tests (SKIP_INTEGRATION_TESTS=1)."
endif

install-git-hooks: ## Install gofmt pre-commit/pre-push hooks
	script/install-git-hooks

# Vagrant: laptop loop is the default (script/vagrant.sh). Smoke is the
# -smoke suffix (script/vagrant-smoke.sh). Runtime state is isolated
# (.vagrant-dev vs .vagrant, 192.168.57 vs .56, ./build-dev vs ./build).
# Source tree and ubuntu_ports_cache are shared.
VAGRANT ?= script/vagrant.sh
VAGRANT_SMOKE ?= script/vagrant-smoke.sh
VM ?=
ITEM ?= quick
ARGS ?=

vagrant: ## Laptop Vagrant loop help (script/vagrant.sh)
	@$(VAGRANT) help

vagrant-setup: ## Boot VMs, build images if needed, bootstrap, connect
	$(VAGRANT) setup $(ARGS)

vagrant-up: ## Boot laptop-loop VMs (dev-builder + dev-node1)
	$(VAGRANT) up $(VM) $(ARGS)

vagrant-status: ## Status of laptop-loop VMs
	$(VAGRANT) status $(ARGS)

vagrant-ssh: ## SSH into a laptop-loop VM (VM=dev-builder)
	$(VAGRANT) ssh $(VM)

vagrant-build: ## Boot builder if needed; build images (works before setup)
	$(VAGRANT) build $(ARGS)

vagrant-cli: ## Build the laptop flynn CLI into /usr/local/bin
	$(VAGRANT) cli $(ARGS)

vagrant-bootstrap: ## Bootstrap the first cluster on dev-builder
	$(VAGRANT) bootstrap $(ARGS)

vagrant-update: ## flynn-host update from the new tarball
	$(VAGRANT) update $(ARGS)

vagrant-reload: ## Reboot laptop-loop VMs and start flynn-host
	$(VAGRANT) reload $(VM) $(ARGS)

vagrant-stop: ## Halt laptop-loop VMs (disks stay)
	$(VAGRANT) stop $(VM) $(ARGS)

vagrant-destroy: ## Delete laptop-loop VMs (./build-dev stays)
	$(VAGRANT) destroy $(VM) $(ARGS)

vagrant-smoke: ## Acceptance suite (--item ITEM, default quick)
	$(VAGRANT_SMOKE) --item $(ITEM) $(ARGS)

vagrant-smoke-list: ## List vagrant-smoke matrix items
	$(VAGRANT_SMOKE) --list $(ARGS)

vagrant-smoke-status: ## Status of smoke VMs
	$(VAGRANT_SMOKE) status $(ARGS)

vagrant-smoke-ssh: ## SSH into a smoke VM (VM=builder or node1)
	$(VAGRANT_SMOKE) ssh $(VM)

vagrant-smoke-up: ## Boot smoke VMs already in .vagrant
	$(VAGRANT_SMOKE) up $(VM) $(ARGS)

vagrant-smoke-reload: ## Reboot smoke VMs
	$(VAGRANT_SMOKE) reload $(VM) $(ARGS)

vagrant-smoke-stop: ## Halt smoke VMs
	$(VAGRANT_SMOKE) stop $(VM) $(ARGS)

vagrant-smoke-destroy: ## Delete smoke VMs (./build stays)
	$(VAGRANT_SMOKE) destroy $(VM) $(ARGS)

# Host-gate contract tests only (no VirtualBox).
test-vagrant: ## Run script/vagrant/test/*.sh (no VMs)
	@for s in script/vagrant/test/*.sh; do bash "$$s" || exit 1; done

help: ## Show this help
	@awk 'BEGIN { \
		FS = ":.*##"; \
		printf "Usage:\n  make <target>\n  make vagrant-smoke ITEM=datastores\n  make vagrant-ssh VM=dev-builder\n\nTargets:\n"; \
	} \
	/^[a-zA-Z0-9_.-]+:.*?##/ { \
		printf "  %-24s %s\n", $$1, $$2; \
	}' $(MAKEFILE_LIST)
	@printf "\nVariables:\n"
	@printf "  %-24s %s\n" "ITEM" "vagrant-smoke matrix item (default: quick)"
	@printf "  %-24s %s\n" "VM" "vagrant / vagrant-smoke machine name"
	@printf "  %-24s %s\n" "ARGS" "extra args forwarded to script/vagrant.sh or vagrant-smoke.sh"
	@printf "  %-24s %s\n" "SKIP_INTEGRATION_TESTS" "set to 1 to skip test-integration"

.PHONY: help build release clean test test-unit test-unit-root test-unit-native test-unit-root-native test-integration install-git-hooks \
	vagrant vagrant-setup vagrant-up vagrant-status vagrant-ssh vagrant-build vagrant-cli vagrant-bootstrap vagrant-update \
	vagrant-reload vagrant-stop vagrant-destroy vagrant-smoke vagrant-smoke-list vagrant-smoke-status vagrant-smoke-ssh \
	vagrant-smoke-up vagrant-smoke-reload vagrant-smoke-stop vagrant-smoke-destroy test-vagrant

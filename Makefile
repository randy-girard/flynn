GO_ENV=GOROOT=`readlink -f build/_go`

build:
	script/build-flynn

release:
	script/build-flynn --git-version

clean:
	script/clean-flynn

# On macOS/Windows, script/run-unit-tests boots a Docker container with the
# Linux test dependencies. On Linux it runs natively (same path as CI).
test: test-unit test-integration

test-unit test-unit-root:
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

test-unit-native: build
	# Exclude host/volume: those need root/ZFS and are run by test-unit-root-native.
	@mkdir -p $(COVERAGE_DIR)
	set +e; \
	env $(GO_ENV) GOFLAGS="-mod=vendor -buildvcs=false" PATH=${PWD}/build/bin:${PATH} \
		go test $(FLYNN_GO_TEST_FLAGS) $(GO_TEST_CHECKPTR) -race $(COVER_UNIT_FLAGS) \
		$$(go list ./... | grep -v '/host/volume'); \
	status=$$?; \
	if [ "$(FLYNN_SKIP_COVERAGE)" != "1" ]; then ./script/report-unit-coverage $(COVERAGE_UNIT); fi; \
	exit $$status

test-unit-root-native: test-unit-native
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
test-integration: build
ifneq ($(SKIP_INTEGRATION_TESTS),1)
	script/run-integration-tests
else
	@echo >&2 "Skipping integration tests (SKIP_INTEGRATION_TESTS=1)."
endif

install-git-hooks:
	script/install-git-hooks

.PHONY: build release clean test test-unit test-unit-root test-unit-native test-unit-root-native test-integration install-git-hooks

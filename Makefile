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
# directly from macOS unless you have installed the Linux appliance deps.
#
# -gcflags=all=-d=checkptr=0: vendored boltdb does unsafe pointer arithmetic
# that Go's checkptr (enabled with -race) rejects as "converted pointer
# straddles multiple allocations". Pass it as a go test arg (not only via
# GOFLAGS) so sudo invocations cannot drop it.
GO_TEST_CHECKPTR=-gcflags=all=-d=checkptr=0

test-unit-native: build
	@test -x /usr/bin/mariabackup || command -v mariabackup >/dev/null || { echo >&2 "MariaDB integration tests require mariabackup (Debian/Ubuntu: apt-get install -y mariadb-backup)"; exit 1; }
	# Exclude host/volume: those need root/ZFS and are run by test-unit-root-native.
	env $(GO_ENV) GOFLAGS=-mod=vendor PATH=${PWD}/build/bin:${PATH} \
		go test $(FLYNN_GO_TEST_FLAGS) $(GO_TEST_CHECKPTR) -race -cover \
		$$(go list ./... | grep -v '/host/volume')

test-unit-root-native: test-unit-native
	sudo -E env $(GO_ENV) GOFLAGS=-mod=vendor PATH=${PWD}/build/bin:${PATH} \
		go test $(FLYNN_GO_TEST_FLAGS) $(GO_TEST_CHECKPTR) -race -cover ./host/volume/...

# Requires Docker, ZFS/volumes, and a functional Flynn host (nested containers).
# Set SKIP_INTEGRATION_TESTS=1 (environment or make argument) to skip cluster bootstrap.
test-integration: build
ifneq ($(SKIP_INTEGRATION_TESTS),1)
	script/run-integration-tests
else
	@echo >&2 "Skipping integration tests (SKIP_INTEGRATION_TESTS=1)."
endif

.PHONY: build release clean test test-unit test-unit-root test-unit-native test-unit-root-native test-integration

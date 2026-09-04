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
test-unit-native: build
	@test -x /usr/bin/mariabackup || command -v mariabackup >/dev/null || { echo >&2 "MariaDB integration tests require mariabackup (Debian/Ubuntu: apt-get install -y mariadb-backup)"; exit 1; }
	env $(GO_ENV) PATH=${PWD}/build/bin:${PATH} GOFLAGS=-gcflags=all=-d=checkptr=0 go test $(FLYNN_GO_TEST_FLAGS) -race -cover ./...

test-unit-root-native: test-unit-native
	sudo -E env $(GO_ENV) PATH=${PWD}/build/bin:${PATH} go test $(FLYNN_GO_TEST_FLAGS) -race -cover ./host/volume/...

# Requires Docker, ZFS/volumes, and a functional Flynn host (nested containers).
# Set SKIP_INTEGRATION_TESTS=1 (environment or make argument) to skip cluster bootstrap.
test-integration: build
ifneq ($(SKIP_INTEGRATION_TESTS),1)
	script/run-integration-tests
else
	@echo >&2 "Skipping integration tests (SKIP_INTEGRATION_TESTS=1)."
endif

.PHONY: build release clean test test-unit test-unit-root test-unit-native test-unit-root-native test-integration

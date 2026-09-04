GO_ENV=GOROOT=`readlink -f build/_go`

build:
	script/build-flynn

release:
	script/build-flynn --git-version

clean:
	script/clean-flynn

test: test-unit test-integration

test-unit: build
	@test -x /usr/bin/mariabackup || { echo >&2 "MariaDB integration tests require mariabackup (Debian/Ubuntu: apt-get install -y mariadb-backup)"; exit 1; }
	env $(GO_ENV) PATH=${PWD}/build/bin:${PATH} GOFLAGS=-gcflags=all=-d=checkptr=0 go test -race -cover ./...

test-unit-root: test-unit
	sudo -E env $(GO_ENV) PATH=${PWD}/build/bin:${PATH} go test -race -cover ./host/volume/...

# Requires Docker, ZFS/volumes, and a functional Flynn host (nested containers).
# Set SKIP_INTEGRATION_TESTS=1 (environment or make argument) to skip cluster bootstrap.
test-integration: build
ifneq ($(SKIP_INTEGRATION_TESTS),1)
	script/run-integration-tests
else
	@echo >&2 "Skipping integration tests (SKIP_INTEGRATION_TESTS=1)."
endif

.PHONY: build release clean test test-unit test-unit-root test-integration

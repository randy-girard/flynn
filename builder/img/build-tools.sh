#!/bin/bash

set -eo pipefail

# Build TUF tools from the local go-tuf folder in the Flynn project
# The go-tuf folder is part of the Flynn module, so we build from the module root

mkdir -p /bin

# Build tuf command from the local go-tuf folder
# Use the Flynn module's vendor directory for dependencies
cd /mnt/src
go build -o /bin/tuf ./go-tuf/cmd/tuf

# Build tuf-client command from the local go-tuf folder
go build -o /bin/tuf-client ./go-tuf/cmd/tuf-client


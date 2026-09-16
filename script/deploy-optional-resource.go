//go:build ignore

// Optional kafka/clickhouse tarball deploys are gone. Install those engines
// with flynn-host plugin install.

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "kafka and clickhouse are plugins. On a cluster host:")
	fmt.Fprintln(os.Stderr, "  sudo flynn-host plugin install kafka")
	fmt.Fprintln(os.Stderr, "  sudo flynn-host plugin install clickhouse")
	os.Exit(1)
}

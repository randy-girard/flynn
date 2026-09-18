package main

import (
	"fmt"

	"github.com/randy-girard/flynn/pkg/version"
)

func init() {
	register("version", runVersion, `
usage: flynn version

Show flynn version string.
`)
}

func runVersion() {
	fmt.Println(version.String())
}

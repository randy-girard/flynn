package main

import (
	"fmt"

	"github.com/flynn/go-docopt"
)

func init() {
	register("install", runInstaller, `
usage: flynn install

Deprecated cluster installer stub. Use the manual installation script instead.
`)
}

func runInstaller(args *docopt.Args) error {
	fmt.Printf("DEPRECATED: `flynn install` has been deprecated.\nRefer to https://github.com/randy-girard/flynn#install-a-cluster for current installation instructions.\n")
	return nil
}

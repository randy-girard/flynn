package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/squashfs"
)

var cmdRun = Command{
	Run: runRun,
	Usage: `
usage: flynn-builder run <args>...

Run a command and generate an image layer.
`[1:],
}

func runRun(args *docopt.Args) error {
	// run the command
	cmdArgs := cliutil.List(args, "<args>")
	var execArgs []string
	if len(cmdArgs) > 1 {
		execArgs = cmdArgs[1:]
	}
	cmd := exec.Command(cmdArgs[0], execArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running the command: %s", err)
	}

	path := "/mnt/out/layer.squashfs"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Squash the overlay diff onto the host bind at /mnt/out (local disk,
		// not 9p). The outer builder then copies this file into layer-cache.
		cmd = mksquashfsCommand(host.DiffPath, path, squashfs.DefaultExcludes())
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, string(out))
			return fmt.Errorf("error running mksquashfs: %s", err)
		}
	}
	return nil
}

// mksquashfsMem caps the cache memory of each mksquashfs invocation. The
// default is 25% of physical RAM *per process*; with APPS_CONCURRENCY layer
// builds running at once (default nproc, 8 on the Vagrant builder) that is
// 2x total RAM and mksquashfs dies with "Write failed because Cannot allocate
// memory" (seen 2026-09-09 building cli-linux-amd64). 1G is ample for a cache.
const mksquashfsMem = squashfs.MemLimit

// mksquashfsCommand builds the mksquashfs command that squashes the container
// diff at diffPath into out, excluding the given top-level paths via stdin.
func mksquashfsCommand(diffPath, out string, excludes []string) *exec.Cmd {
	cmd := exec.Command("mksquashfs", squashfs.Args(diffPath, out,
		"-mem", mksquashfsMem,
		"-ef", "/dev/stdin",
	)...)
	cmd.Stdin = strings.NewReader(strings.Join(excludes, "\n"))
	return cmd
}

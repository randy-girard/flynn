package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/flynn/flynn/pkg/cliutil"
	"github.com/flynn/go-docopt"
)

var usage = `
usage: flynn-builder <command> [<args>...]

Commands:
  build      build Flynn images
  run        run a command and generate an image layer
`[1:]

type Command struct {
	Run   func(args *docopt.Args) error
	Usage string
}

// startPprof exposes net/http/pprof when FLYNN_BUILDER_PPROF_ADDR is set (e.g.
// "127.0.0.1:6060"). flynn-builder has been observed at >20 GB RSS during
// `build --only=apps` on the Vagrant builder, starving mksquashfs/9p of memory;
// this lets a heap profile be taken from a running build without a rebuild.
func startPprof() {
	addr := os.Getenv("FLYNN_BUILDER_PPROF_ADDR")
	if addr == "" {
		return
	}
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("pprof listener on %s failed: %v", addr, err)
		}
	}()
}

func main() {
	startPprof()
	args, _ := docopt.Parse(usage, nil, true, "", true)

	name := args.String["<command>"]
	var cmd Command
	switch name {
	case "build":
		cmd = cmdBuild
	case "run":
		cmd = cmdRun
	default:
		fmt.Fprintln(os.Stderr, usage)
		log.Fatalf("unknown command %q", name)
	}

	args, _ = docopt.Parse(cmd.Usage, append([]string{name}, cliutil.List(args, "<args>")...), true, "", true)
	if err := cmd.Run(args); err != nil {
		log.Fatal(err)
	}
}

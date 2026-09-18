package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/cheggaaa/pb"
	"github.com/flynn/flynn/pkg/backup"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/term"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("backup", runBackup, `
usage: flynn-host backup [--file <file>]

Take a backup of the cluster.

The backup may be restored while creating a new cluster with
'flynn-host bootstrap --from-backup'.

Options:
	--file=<backup-file>  file to write backup to (defaults to stdout)
`)
}

func runBackup(args *docopt.Args, _ *cluster.Client) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}

	var bar *pb.ProgressBar
	var progress backup.ProgressBar
	if term.IsTerminal(os.Stderr.Fd()) {
		bar = pb.New(0)
		bar.SetUnits(pb.U_BYTES)
		bar.ShowBar = false
		bar.ShowSpeed = true
		bar.Output = os.Stderr
		bar.Start()
		progress = bar
	}

	var dest io.Writer = os.Stdout
	if filename := args.String["--file"]; filename != "" {
		f, err := os.Create(filename)
		if err != nil {
			return err
		}
		defer f.Close()
		dest = f
	}

	fmt.Fprintln(os.Stderr, "Creating cluster backup...")
	if err := backup.Run(client, dest, progress); err != nil {
		return err
	}
	if bar != nil {
		bar.Finish()
	}
	fmt.Fprintln(os.Stderr, "Backup complete.")
	return nil
}

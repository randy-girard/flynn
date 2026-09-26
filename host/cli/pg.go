package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/httpclient"
	"github.com/randy-girard/flynn/pkg/platformpg"
	"github.com/randy-girard/flynn/pkg/term"
)

func init() {
	Register("pg:psql", runHostPgPsql, `
usage: flynn-host pg:psql [--] [<argument>...]

Open a psql console to the platform Postgres database (the controller database
on the built-in appliance). Tenant databases use flynn pg from the postgres plugin.

Examples:

    $ flynn-host pg:psql
    $ flynn-host pg:psql -- -c "SELECT 1"
`)
	Register("pg:dump", runHostPgDump, `
usage: flynn-host pg:dump [-q] [-f <file>]

Dump the platform Postgres database. If --file is omitted, the dump goes to stdout.

Options:
	-f, --file=<file>  name of dump file
	-q, --quiet        don't print progress
`)
	Register("pg:restore", runHostPgRestore, `
usage: flynn-host pg:restore [-q] [-j <jobs>] [-f <file>]

Restore a dump into the platform Postgres database. If --file is omitted, the dump is read from stdin.

Options:
	-f, --file=<file>  name of dump file
	-q, --quiet        don't print progress
	-j, --jobs=<jobs>  number of pg_restore jobs to use [default: 1]
`)
}

type platformPgClient interface {
	GetAppRelease(appID string) (*ct.Release, error)
	RunJobAttached(appID string, job *ct.NewJob) (httpclient.ReadWriteCloser, error)
}

func runHostPgPsql(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	return hostPgPsql(client, cliutil.List(args, "<argument>"), os.Stdin, os.Stdout, os.Stderr)
}

func runHostPgDump(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	out := io.Writer(os.Stdout)
	if filename := args.String["--file"]; filename != "" {
		f, err := os.Create(filename)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	return hostPgDump(client, out, os.Stderr, args.Bool["--quiet"])
}

func runHostPgRestore(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	jobs, err := strconv.Atoi(args.String["--jobs"])
	if err != nil {
		return err
	}
	in := io.Reader(os.Stdin)
	if filename := args.String["--file"]; filename != "" {
		f, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	return hostPgRestore(client, in, os.Stdout, os.Stderr, jobs, args.Bool["--quiet"])
}

func platformJob(client platformPgClient, kind string, psqlArgs []string, jobs int) (platformpg.Job, error) {
	ctrl, err := client.GetAppRelease("controller")
	if err != nil {
		return platformpg.Job{}, fmt.Errorf("error getting controller release: %s", err)
	}
	app := ""
	if ctrl.Env != nil {
		app = ctrl.Env["FLYNN_POSTGRES"]
	}
	var pgReleaseID string
	if app != "" {
		pgRel, err := client.GetAppRelease(app)
		if err != nil {
			return platformpg.Job{}, fmt.Errorf("error getting platform postgres release: %s", err)
		}
		pgReleaseID = pgRel.ID
	}
	return platformpg.FromController(ctrl.Env, pgReleaseID, kind, psqlArgs, jobs)
}

func hostPgPsql(client platformPgClient, psqlArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	job, err := platformJob(client, "psql", psqlArgs, 1)
	if err != nil {
		return err
	}
	req := newPlatformJob(job)
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(f.Fd()) {
		req.TTY = true
	}
	return runPlatformJob(client, job.App, req, stdin, stdout, stderr, false)
}

func hostPgDump(client platformPgClient, stdout, stderr io.Writer, quiet bool) error {
	job, err := platformJob(client, "dump", nil, 1)
	if err != nil {
		return err
	}
	if !quiet {
		fmt.Fprintln(stderr, "Dumping the platform database...")
	}
	return runPlatformJob(client, job.App, newPlatformJob(job), nil, stdout, stderr, false)
}

func hostPgRestore(client platformPgClient, stdin io.Reader, stdout, stderr io.Writer, jobs int, quiet bool) error {
	job, err := platformJob(client, "restore", nil, jobs)
	if err != nil {
		return err
	}
	if !quiet {
		fmt.Fprintln(stderr, "Restoring the platform database...")
	}
	return runPlatformJob(client, job.App, newPlatformJob(job), stdin, stdout, stderr, true)
}

func newPlatformJob(job platformpg.Job) *ct.NewJob {
	return &ct.NewJob{
		ReleaseID:            job.ReleaseID,
		Args:                 job.Args,
		Env:                  job.Env,
		DisableLog:           true,
		Data:                 job.Data,
		DeprecatedEntrypoint: []string{job.Args[0]},
		DeprecatedCmd:        job.Args[1:],
	}
}

func runPlatformJob(client platformPgClient, app string, req *ct.NewJob, stdin io.Reader, stdout, stderr io.Writer, restore bool) error {
	rwc, err := client.RunJobAttached(app, req)
	if err != nil {
		return fmt.Errorf("error running platform postgres job: %s", err)
	}
	defer rwc.Close()
	attach := cluster.NewAttachClient(rwc)
	if stdin != nil {
		go func() {
			_, _ = io.Copy(attach, stdin)
			_ = attach.CloseWrite()
		}()
	} else {
		_ = attach.CloseWrite()
	}
	exitStatus, err := attach.Receive(stdout, stderr)
	if err != nil {
		return err
	}
	if restore && exitStatus == 1 {
		// pg_restore exits 1 when there are warnings.
		return nil
	}
	if exitStatus != 0 {
		return fmt.Errorf("platform postgres job exited with status %d", exitStatus)
	}
	return nil
}

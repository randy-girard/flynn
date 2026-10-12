package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/flynn/go-docopt"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// pgCaughtUpBytes is one WAL segment: a replica this close to the primary is
// caught up, since a busy primary keeps writing between the two status reads.
const pgCaughtUpBytes = 16 << 20

func init() {
	Register("pg:status", runHostPgStatus, `
usage: flynn-host pg:status

Show each platform Postgres peer's role and replication progress: the
pg_basebackup percent while a replica copies from its upstream, then how far
it is behind the primary while it streams WAL.
`)
	Register("pg:wait", runHostPgWait, `
usage: flynn-host pg:wait [--timeout=<duration>]

Print platform Postgres replication progress every 2 seconds until the primary
is read-write and every replica is running and within one WAL segment (16 MB)
of the primary.

Options:
	--timeout=<duration>  give up after this long [default: 30m]
`)
}

type pgPeerRow struct {
	Addr     string
	Role     string
	Progress string
	Ready    bool
}

func pgPeerStatuses() ([]*discoverd.Instance, func(string) (*sireniaclient.Status, error), error) {
	instances, err := discoverd.NewService("postgres").Instances()
	if err != nil {
		return nil, nil, fmt.Errorf("discover postgres instances: %w", err)
	}
	return instances, func(addr string) (*sireniaclient.Status, error) {
		return sireniaclient.NewClient(addr).Status()
	}, nil
}

// pgStatusRows queries every peer and describes it relative to the primary.
func pgStatusRows(instances []*discoverd.Instance, status func(string) (*sireniaclient.Status, error), tracker *sireniaclient.CatchupTracker) ([]pgPeerRow, bool) {
	type peer struct {
		addr string
		st   *sireniaclient.Status
		err  error
	}
	peers := make([]peer, 0, len(instances))
	primaryXLog := ""
	hasPrimary := false
	for _, inst := range instances {
		st, err := status(inst.Addr)
		peers = append(peers, peer{inst.Addr, st, err})
		if err == nil && st != nil && st.Peer != nil && st.Peer.Role == state.RolePrimary && st.Database != nil {
			hasPrimary = st.Database.Running && st.Database.ReadWrite
			primaryXLog = st.Database.XLog
		}
	}
	rows := make([]pgPeerRow, 0, len(peers))
	allReady := hasPrimary && len(peers) > 0
	for _, p := range peers {
		row := pgPeerRow{Addr: p.addr, Role: "unknown"}
		if p.st != nil && p.st.Peer != nil {
			row.Role = p.st.Peer.Role.String()
		}
		db := (*sireniaclient.DatabaseInfo)(nil)
		if p.st != nil {
			db = p.st.Database
		}
		switch {
		case p.err != nil:
			row.Progress = "unreachable: " + p.err.Error()
		case db == nil:
			row.Progress = "postgres not ready"
		case db.Basebackup != nil:
			row.Progress = sireniaclient.FormatBasebackup(db.Basebackup)
		case !db.Running:
			row.Progress = "postgres not running"
		case row.Role == state.RolePrimary.String():
			row.Ready = db.ReadWrite
			row.Progress = fmt.Sprintf("read-write=%v xlog=%s", db.ReadWrite, db.XLog)
		default:
			row.Progress, row.Ready = pgReplicaProgress(p.st, primaryXLog, tracker)
		}
		allReady = allReady && row.Ready
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return pgRoleOrder(rows[i].Role) < pgRoleOrder(rows[j].Role) })
	return rows, allReady
}

func pgReplicaProgress(st *sireniaclient.Status, primaryXLog string, tracker *sireniaclient.CatchupTracker) (string, bool) {
	lag, ok := sireniaclient.LagBytes(primaryXLog, st.Database.XLog)
	if !ok {
		return fmt.Sprintf("streaming WAL xlog=%s", st.Database.XLog), false
	}
	if lag <= pgCaughtUpBytes {
		tracker.Percent(st.Peer.ID, 0)
		return fmt.Sprintf("caught up, %s behind xlog=%s", sireniaclient.FormatBytes(lag), st.Database.XLog), true
	}
	if pct := tracker.Percent(st.Peer.ID, lag); pct >= 0 {
		return fmt.Sprintf("catching up %d%%, %s behind xlog=%s", pct, sireniaclient.FormatBytes(lag), st.Database.XLog), false
	}
	return fmt.Sprintf("catching up, %s behind xlog=%s", sireniaclient.FormatBytes(lag), st.Database.XLog), false
}

func pgRoleOrder(role string) int {
	switch role {
	case "primary":
		return 0
	case "sync":
		return 1
	case "async":
		return 2
	}
	return 3
}

func writePgStatus(w io.Writer, rows []pgPeerRow) {
	tw := tabwriter.NewWriter(w, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDR\tROLE\tPROGRESS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Addr, r.Role, r.Progress)
	}
	tw.Flush()
}

func runHostPgStatus(_ *docopt.Args) error {
	instances, status, err := pgPeerStatuses()
	if err != nil {
		return err
	}
	rows, _ := pgStatusRows(instances, status, nil)
	writePgStatus(os.Stdout, rows)
	return nil
}

func runHostPgWait(args *docopt.Args) error {
	timeout, err := time.ParseDuration(args.String["--timeout"])
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
	}
	return hostPgWait(os.Stdout, timeout, 2*time.Second, pgPeerStatuses)
}

func hostPgWait(out io.Writer, timeout, interval time.Duration, discover func() ([]*discoverd.Instance, func(string) (*sireniaclient.Status, error), error)) error {
	tracker := &sireniaclient.CatchupTracker{}
	deadline := time.Now().Add(timeout)
	for {
		instances, status, err := discover()
		if err != nil {
			fmt.Fprintf(out, "%s %s\n", time.Now().UTC().Format(time.RFC3339), err)
		} else {
			rows, ready := pgStatusRows(instances, status, tracker)
			fmt.Fprintln(out, time.Now().UTC().Format(time.RFC3339))
			writePgStatus(out, rows)
			if ready {
				fmt.Fprintln(out, "platform postgres replication is caught up")
				return nil
			}
		}
		if time.Now().Add(interval).After(deadline) {
			return fmt.Errorf("timed out after %s waiting for platform postgres replication", timeout)
		}
		time.Sleep(interval)
	}
}

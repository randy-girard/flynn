package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("metrics", runHostMetrics, `
usage: flynn-host metrics [--json] [--host <id>]

Print a live cluster metrics snapshot from flynn-host.

Without --host, every node is sampled. Use --json for the raw host stats.

Options:
    --json        Print raw JSON
    --host=<id>   Sample one host id
`)
}

func runHostMetrics(args *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return err
	}
	want := strings.TrimSpace(args.String["--host"])
	var stats []*host.HostResourceStats
	var first error
	for _, h := range hosts {
		if want != "" && h.ID() != want {
			continue
		}
		row, err := h.GetStats()
		if err != nil {
			if first == nil {
				first = fmt.Errorf("host %s: %w", h.ID(), err)
			}
			continue
		}
		stats = append(stats, row)
	}
	if want != "" && len(stats) == 0 {
		if first != nil {
			return first
		}
		return fmt.Errorf("host %q not found or did not return stats", want)
	}
	if len(stats) == 0 {
		if first != nil {
			return first
		}
		return fmt.Errorf("no hosts returned stats")
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].HostID < stats[j].HostID })
	if args.Bool["--json"] {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}
	return writeHostMetricsTable(os.Stdout, stats)
}

func writeHostMetricsTable(w io.Writer, stats []*host.HostResourceStats) error {
	tw := tabwriter.NewWriter(w, 1, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tCPU\tMEMORY\tDISK\tLOAD1\tJOBS")
	for _, s := range stats {
		if s == nil {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.2f\t%d\n",
			s.HostID,
			fmt.Sprintf("%.1f%%", s.CPUUsagePercent),
			formatMemPair(s.MemoryUsedBytes, s.MemoryTotalBytes),
			formatDiskPercent(s.DiskUsedBytes, s.DiskTotalBytes),
			s.LoadAvg1,
			s.RunningJobsCount,
		)
	}
	return tw.Flush()
}

func formatMemPair(used, total uint64) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%s/%s (%.0f%%)", formatBytesIEC(used), formatBytesIEC(total), 100*float64(used)/float64(total))
}

func formatDiskPercent(used, total uint64) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(used)/float64(total))
}

func formatBytesIEC(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGT"[exp])
}

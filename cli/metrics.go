package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
)

func init() {
	register("metrics", runAppMetrics, `
usage: flynn metrics [--json] [-t <proc>]

Print the latest stored app metrics snapshot from the dashboard.

This is the same sample the app alerts evaluate. Cluster host snapshots
use flynn-host metrics.

Options:
    --json                 Print raw JSON
    -t, --process-type=<proc>  Show one process type
`)
}

type appMetricSnapshot struct {
	ID          int64           `json:"id"`
	AppID       string          `json:"app_id"`
	CollectedAt time.Time       `json:"collected_at"`
	Aggregates  json.RawMessage `json:"aggregates"`
}

type appMetricAgg struct {
	ContainerCount          float64  `json:"containerCount"`
	AvgCpu                  *float64 `json:"avgCpu"`
	AvgMemOfLimitPercent    *float64 `json:"avgMemOfLimitPercent"`
	AvgMemBytesPerContainer float64  `json:"avgMemBytesPerContainer"`
	HostLoadAvg1            *float64 `json:"hostLoadAvg1"`
	HostLoadAvg5            *float64 `json:"hostLoadAvg5"`
	HostLoadAvg15           *float64 `json:"hostLoadAvg15"`
	ResponseP50Ms           *float64 `json:"responseP50Ms"`
	ResponseP95Ms           *float64 `json:"responseP95Ms"`
	ResponseP99Ms           *float64 `json:"responseP99Ms"`
}

func runAppMetrics(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	hc, base, key, err := dashboardAPI()
	if err != nil {
		return err
	}
	var snap appMetricSnapshot
	if err := dashboardDoJSON(hc, http.MethodGet, base+"/api/apps/"+app.ID+"/metrics/snapshot", key, nil, &snap); err != nil {
		return err
	}
	if args.Bool["--json"] {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}
	return writeAppMetricSnapshot(os.Stdout, snap, strings.TrimSpace(args.String["--process-type"]))
}

func writeAppMetricSnapshot(w io.Writer, snap appMetricSnapshot, processType string) error {
	var byType map[string]appMetricAgg
	if len(snap.Aggregates) > 0 {
		if err := json.Unmarshal(snap.Aggregates, &byType); err != nil {
			return err
		}
	}
	if processType != "" {
		agg, ok := byType[processType]
		if !ok {
			return fmt.Errorf("no sample for process type %q", processType)
		}
		byType = map[string]appMetricAgg{processType: agg}
	}
	keys := make([]string, 0, len(byType))
	for k := range byType {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !snap.CollectedAt.IsZero() {
		fmt.Fprintf(w, "collected %s\n", snap.CollectedAt.UTC().Format(time.RFC3339))
	}
	tw := tabwriter.NewWriter(w, 1, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tCONTAINERS\tCPU\tMEM\tLOAD1\tP95")
	for _, k := range keys {
		a := byType[k]
		fmt.Fprintf(tw, "%s\t%.0f\t%s\t%s\t%s\t%s\n",
			k,
			a.ContainerCount,
			formatOptionalPercent(a.AvgCpu),
			formatMemOfLimit(a),
			formatOptionalFixed(a.HostLoadAvg1),
			formatOptionalMS(a.ResponseP95Ms),
		)
	}
	return tw.Flush()
}

func formatOptionalPercent(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func formatOptionalFixed(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

func formatOptionalMS(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0fms", *v)
}

func formatMemOfLimit(a appMetricAgg) string {
	if a.AvgMemOfLimitPercent != nil {
		return fmt.Sprintf("%.1f%%", *a.AvgMemOfLimitPercent)
	}
	if a.AvgMemBytesPerContainer > 0 {
		return formatBytesIEC(uint64(a.AvgMemBytesPerContainer))
	}
	return "—"
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

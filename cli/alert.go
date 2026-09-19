package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
)

func init() {
	register("alert", runAppAlertList, `
usage: flynn alert

List metric alerts for the app.

Requires the dashboard plugin. Cluster alerts use flynn-host alert.
`)
	register("alert:add", runAppAlertAdd, `
usage: flynn alert:add --metric <metric> --op <op> --threshold <n> [-t <proc>] [--name <name>] [--cooldown <sec>] [--email <addr>] [--webhook <url>]

Create an app metric alert. At least one of --email or --webhook is required.

Metrics: cpu_percent, memory_percent, memory_bytes, containers, host_load_1,
host_load_5, host_load_15, response_p50_ms, response_p95_ms, response_p99_ms.

Operators: gt, gte, lt, lte.

Options:
    -t, --process-type=<proc>  Process type (defaults to all)
    --metric=<metric>          App metric id
    --op=<op>                  gt, gte, lt, or lte
    --threshold=<n>            Threshold value
    --name=<name>              Display name (defaults to metric + operator + threshold)
    --cooldown=<sec>           Repeat notify cooldown [default: 300]
    --email=<addr>             Email when the rule fires
    --webhook=<url>            HTTP webhook when the rule fires

Examples:

    $ flynn -a demo alert:add --metric cpu_percent --op gt --threshold 80 --email ops@example.com
`)
	register("alert:enable", runAppAlertEnable, `
usage: flynn alert:enable <id>

Enable an app metric alert.
`)
	register("alert:disable", runAppAlertDisable, `
usage: flynn alert:disable <id>

Disable an app metric alert.
`)
	register("alert:remove", runAppAlertRemove, `
usage: flynn alert:remove <id>

Delete an app metric alert.
`)
}

type dashboardMetricAlert struct {
	ID              string   `json:"id"`
	Scope           string   `json:"scope"`
	AppID           string   `json:"app_id,omitempty"`
	Name            string   `json:"name"`
	ProcessType     string   `json:"process_type,omitempty"`
	HostID          string   `json:"host_id,omitempty"`
	Metric          string   `json:"metric"`
	Operator        string   `json:"operator"`
	Threshold       float64  `json:"threshold"`
	CooldownSeconds int      `json:"cooldown_seconds"`
	NotifyEmail     bool     `json:"notify_email"`
	EmailTo         string   `json:"email_to,omitempty"`
	NotifyWebhook   bool     `json:"notify_webhook"`
	WebhookURL      string   `json:"webhook_url,omitempty"`
	Enabled         bool     `json:"enabled"`
	Firing          bool     `json:"firing"`
	LastValue       *float64 `json:"last_value,omitempty"`
	LastError       string   `json:"last_error,omitempty"`
}

func runAppAlertList(_ *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	hc, base, key, err := dashboardAPI()
	if err != nil {
		return err
	}
	var rows []dashboardMetricAlert
	if err := dashboardDoJSON(hc, http.MethodGet, base+"/api/apps/"+app.ID+"/alerts", key, nil, &rows); err != nil {
		return err
	}
	return writeAlertTable(os.Stdout, rows)
}

func runAppAlertAdd(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	body, err := appAlertCreateBody(args)
	if err != nil {
		return err
	}
	hc, base, key, err := dashboardAPI()
	if err != nil {
		return err
	}
	var created dashboardMetricAlert
	if err := dashboardDoJSON(hc, http.MethodPost, base+"/api/apps/"+app.ID+"/alerts", key, body, &created); err != nil {
		return err
	}
	fmt.Printf("Created alert %s.\n", created.ID)
	return nil
}

func runAppAlertEnable(args *docopt.Args, client controller.Client) error {
	return patchAppAlert(client, args.String["<id>"], map[string]any{"enabled": true})
}

func runAppAlertDisable(args *docopt.Args, client controller.Client) error {
	return patchAppAlert(client, args.String["<id>"], map[string]any{"enabled": false})
}

func runAppAlertRemove(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	hc, base, key, err := dashboardAPI()
	if err != nil {
		return err
	}
	id := strings.TrimSpace(args.String["<id>"])
	if err := dashboardDoJSON(hc, http.MethodDelete, base+"/api/apps/"+app.ID+"/alerts/"+id, key, nil, nil); err != nil {
		return err
	}
	fmt.Printf("Deleted alert %s.\n", id)
	return nil
}

func patchAppAlert(client controller.Client, id string, body map[string]any) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	hc, base, key, err := dashboardAPI()
	if err != nil {
		return err
	}
	var out dashboardMetricAlert
	if err := dashboardDoJSON(hc, http.MethodPatch, base+"/api/apps/"+app.ID+"/alerts/"+strings.TrimSpace(id), key, body, &out); err != nil {
		return err
	}
	state := "disabled"
	if out.Enabled {
		state = "enabled"
	}
	fmt.Printf("Alert %s is %s.\n", out.ID, state)
	return nil
}

func appAlertCreateBody(args *docopt.Args) (map[string]any, error) {
	metric := strings.TrimSpace(args.String["--metric"])
	op := strings.ToLower(strings.TrimSpace(args.String["--op"]))
	threshold, err := strconv.ParseFloat(strings.TrimSpace(args.String["--threshold"]), 64)
	if err != nil {
		return nil, fmt.Errorf("threshold must be a number")
	}
	name := strings.TrimSpace(args.String["--name"])
	if name == "" {
		name = fmt.Sprintf("%s %s %g", metric, op, threshold)
	}
	email := strings.TrimSpace(args.String["--email"])
	webhook := strings.TrimSpace(args.String["--webhook"])
	if email == "" && webhook == "" {
		return nil, fmt.Errorf("specify --email and/or --webhook")
	}
	cooldown := 300
	if raw := strings.TrimSpace(args.String["--cooldown"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("cooldown must be a non-negative number of seconds")
		}
		cooldown = n
	}
	proc := strings.TrimSpace(args.String["--process-type"])
	if proc == "" {
		proc = "all"
	}
	body := map[string]any{
		"name":             name,
		"metric":           metric,
		"operator":         op,
		"threshold":        threshold,
		"cooldown_seconds": cooldown,
		"process_type":     proc,
	}
	if email != "" {
		body["notify_email"] = true
		body["email_to"] = email
	}
	if webhook != "" {
		body["notify_webhook"] = true
		body["webhook_url"] = webhook
	}
	return body, nil
}

func writeAlertTable(w io.Writer, rows []dashboardMetricAlert) error {
	tw := tabwriter.NewWriter(w, 1, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tMETRIC\tWHEN\tNOTIFY\tSTATE")
	for _, a := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.Name, alertMetricCell(a), alertWhenCell(a), alertNotifyCell(a), alertStateCell(a),
		)
	}
	return tw.Flush()
}

func alertMetricCell(a dashboardMetricAlert) string {
	if a.HostID != "" {
		return a.Metric + "@" + a.HostID
	}
	if a.ProcessType != "" && a.ProcessType != "all" {
		return a.Metric + "/" + a.ProcessType
	}
	return a.Metric
}

func alertWhenCell(a dashboardMetricAlert) string {
	return fmt.Sprintf("%s %g", strings.ToUpper(a.Operator), a.Threshold)
}

func alertNotifyCell(a dashboardMetricAlert) string {
	var parts []string
	if a.NotifyEmail {
		parts = append(parts, a.EmailTo)
	}
	if a.NotifyWebhook {
		parts = append(parts, "webhook")
	}
	return strings.Join(parts, ",")
}

func alertStateCell(a dashboardMetricAlert) string {
	if !a.Enabled {
		return "disabled"
	}
	if a.Firing {
		return "firing"
	}
	return "ok"
}

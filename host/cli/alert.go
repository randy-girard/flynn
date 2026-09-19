package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
)

func init() {
	Register("alert", runHostAlertList, `
usage: flynn-host alert

List cluster metric alerts stored by the dashboard.

Requires the dashboard plugin. App alerts use flynn alert.
`)
	Register("alert:add", runHostAlertAdd, `
usage: flynn-host alert:add --metric <metric> --op <op> --threshold <n> [--name <name>] [--host <id>] [--cooldown <sec>] [--email <addr>] [--webhook <url>]

Create a cluster metric alert. At least one of --email or --webhook is required.

Metrics: cpu_percent, memory_percent, disk_percent, host_load_1, host_load_5,
host_load_15, running_jobs, job_containers.

Operators: gt, gte, lt, lte.

Options:
    --metric=<metric>      Cluster metric id
    --op=<op>              gt, gte, lt, or lte
    --threshold=<n>        Threshold value
    --name=<name>          Display name (defaults to metric + operator + threshold)
    --host=<id>            Limit the rule to one host id
    --cooldown=<sec>       Repeat notify cooldown [default: 300]
    --email=<addr>         Email when the rule fires
    --webhook=<url>        HTTP webhook when the rule fires

Examples:

    $ flynn-host alert:add --metric disk_percent --op gte --threshold 90 --email ops@example.com
`)
	Register("alert:enable", runHostAlertEnable, `
usage: flynn-host alert:enable <id>

Enable a cluster metric alert.
`)
	Register("alert:disable", runHostAlertDisable, `
usage: flynn-host alert:disable <id>

Disable a cluster metric alert.
`)
	Register("alert:remove", runHostAlertRemove, `
usage: flynn-host alert:remove <id>

Delete a cluster metric alert.
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

func runHostAlertList(_ *docopt.Args) error {
	client, base, key, err := dashboardPluginClient()
	if err != nil {
		return err
	}
	var rows []dashboardMetricAlert
	if err := dashboardDoJSON(client, http.MethodGet, base+"/api/alerts", key, nil, &rows); err != nil {
		return err
	}
	return writeAlertTable(os.Stdout, rows)
}

func runHostAlertAdd(args *docopt.Args) error {
	body, err := alertCreateBody(args, "")
	if err != nil {
		return err
	}
	client, base, key, err := dashboardPluginClient()
	if err != nil {
		return err
	}
	var created dashboardMetricAlert
	if err := dashboardDoJSON(client, http.MethodPost, base+"/api/alerts", key, body, &created); err != nil {
		return err
	}
	fmt.Printf("Created alert %s.\n", created.ID)
	return nil
}

func runHostAlertEnable(args *docopt.Args) error {
	return patchHostAlert(args.String["<id>"], map[string]any{"enabled": true})
}

func runHostAlertDisable(args *docopt.Args) error {
	return patchHostAlert(args.String["<id>"], map[string]any{"enabled": false})
}

func runHostAlertRemove(args *docopt.Args) error {
	client, base, key, err := dashboardPluginClient()
	if err != nil {
		return err
	}
	id := strings.TrimSpace(args.String["<id>"])
	if err := dashboardDoJSON(client, http.MethodDelete, base+"/api/alerts/"+id, key, nil, nil); err != nil {
		return err
	}
	fmt.Printf("Deleted alert %s.\n", id)
	return nil
}

func patchHostAlert(id string, body map[string]any) error {
	client, base, key, err := dashboardPluginClient()
	if err != nil {
		return err
	}
	var out dashboardMetricAlert
	if err := dashboardDoJSON(client, http.MethodPatch, base+"/api/alerts/"+strings.TrimSpace(id), key, body, &out); err != nil {
		return err
	}
	state := "disabled"
	if out.Enabled {
		state = "enabled"
	}
	fmt.Printf("Alert %s is %s.\n", out.ID, state)
	return nil
}

func alertCreateBody(args *docopt.Args, processType string) (map[string]any, error) {
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
	body := map[string]any{
		"name":             name,
		"metric":           metric,
		"operator":         op,
		"threshold":        threshold,
		"cooldown_seconds": cooldown,
	}
	if hostID := strings.TrimSpace(args.String["--host"]); hostID != "" {
		body["host_id"] = hostID
	}
	if processType != "" {
		body["process_type"] = processType
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

func dashboardDoJSON(client *http.Client, method, rawURL, key string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth("", key)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = res.Status
		}
		var wrapped struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &wrapped) == nil && wrapped.Error != "" {
			msg = wrapped.Error
		}
		return fmt.Errorf("dashboard: %s", msg)
	}
	if out == nil || res.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return nil
}

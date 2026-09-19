package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func init() {
	Register("events", runEventsListCmd, `
usage: flynn-host events

Show which flynn-host webhook codes the dashboard event chart displays.

Requires the dashboard plugin.
`)
	Register("events:visible", runEventsVisibleCmd, `
usage: flynn-host events:visible [--all] [<code>...]

Set which event types the dashboard event chart shows. Requires dashboard
administrator / cluster key access.

Options:
    --all  Show every known event type

Examples:

    $ flynn-host events:visible --all
    $ flynn-host events:visible H18 H19 H13 H21 D20
`)
}

type eventChartCode struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	Visible bool   `json:"visible"`
}

type eventChartSettings struct {
	Codes []string         `json:"codes"`
	All   bool             `json:"all"`
	Known []eventChartCode `json:"known"`
}

func runEventsListCmd(_ *docopt.Args) error {
	client, base, key, err := dashboardPluginClient()
	if err != nil {
		return err
	}
	settings, err := getEventChartSettings(client, base, key)
	if err != nil {
		return err
	}
	mode := "all"
	if !settings.All {
		mode = strings.Join(settings.Codes, ",")
	}
	fmt.Printf("visible=%s\n", mode)
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tLABEL\tVISIBLE")
	for _, row := range settings.Known {
		fmt.Fprintf(w, "%s\t%s\t%t\n", row.Code, row.Label, row.Visible)
	}
	return w.Flush()
}

func runEventsVisibleCmd(args *docopt.Args) error {
	client, base, key, err := dashboardPluginClient()
	if err != nil {
		return err
	}
	all := args.Bool["--all"]
	codes := cliutil.List(args, "<code>")
	if all {
		codes = nil
	} else if len(codes) == 0 {
		return fmt.Errorf("specify event codes (H18 H19 …) or --all")
	}
	settings, err := putEventChartSettings(client, base, key, eventChartSettings{Codes: codes, All: all})
	if err != nil {
		return err
	}
	if settings.All {
		fmt.Println("visible=all")
		return nil
	}
	fmt.Printf("visible=%s\n", strings.Join(settings.Codes, ","))
	return nil
}

func dashboardPluginClient() (*http.Client, string, string, error) {
	ctrl, err := controllerClient()
	if err != nil {
		return nil, "", "", err
	}
	apps, err := ctrl.AppList()
	if err != nil {
		return nil, "", "", err
	}
	app, err := plugin.LookupPluginApp(apps, "dashboard")
	if err != nil {
		return nil, "", "", fmt.Errorf("the dashboard plugin is not installed\nInstall it with: sudo flynn-host plugin:install dashboard")
	}
	key, err := controllerAuthKey()
	if err != nil {
		return nil, "", "", err
	}
	return discoverdHTTPClient(), dashboardPluginBase(app), key, nil
}

func dashboardPluginBase(app *ct.App) string {
	if app != nil && app.Meta != nil {
		if ping := strings.TrimSpace(app.Meta[plugin.MetaPluginWait]); ping != "" {
			if u, err := url.Parse(ping); err == nil && u.Scheme != "" && u.Host != "" {
				u.Path = ""
				u.RawQuery = ""
				u.Fragment = ""
				return strings.TrimRight(u.String(), "/")
			}
		}
	}
	name := "dashboard"
	if app != nil && app.Name != "" {
		name = app.Name
	}
	return "http://" + name + ".discoverd"
}

func controllerAuthKey() (string, error) {
	instances, err := discoverd.NewService("controller").Instances()
	if err != nil {
		return "", err
	}
	if len(instances) == 0 || instances[0].Meta == nil {
		return "", fmt.Errorf("controller AUTH_KEY is unavailable")
	}
	key := strings.TrimSpace(instances[0].Meta["AUTH_KEY"])
	if key == "" {
		return "", fmt.Errorf("controller AUTH_KEY is unavailable")
	}
	return key, nil
}

func getEventChartSettings(client *http.Client, base, key string) (*eventChartSettings, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/settings/event-chart", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("", key)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return decodeEventChartSettings(res)
}

func putEventChartSettings(client *http.Client, base, key string, body eventChartSettings) (*eventChartSettings, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPut, strings.TrimRight(base, "/")+"/api/settings/event-chart", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", key)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return decodeEventChartSettings(res)
}

func decodeEventChartSettings(res *http.Response) (*eventChartSettings, error) {
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = res.Status
		}
		return nil, fmt.Errorf("dashboard event chart: %s", msg)
	}
	var out eventChartSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/cliutil"
	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("otel", runOTEL, `
usage: flynn-host otel
       flynn-host otel add [--header <header>]... [--insecure] <endpoint>
       flynn-host otel remove <id>

Forward Flynn cluster metrics to an OpenTelemetry collector (OTLP/HTTP JSON).
Requires the otel plugin:

    sudo flynn-host plugin install otel

The plugin polls GET /cluster/stats and GET /cluster/jobs-stats and POSTs
/v1/metrics. Job logs stay on flynn-host log-sink / flynn logsink (syslog).

Options:
    --header=<header>  Extra HTTP header "Name: value" (repeatable)
    --insecure         Skip TLS verification of the collector

Examples:

    $ flynn-host otel add http://alloy.example:4318
    $ flynn-host otel add --header "Authorization: Bearer TOKEN" https://otlp.grafana.net/otlp
`)
}

type otelExporter struct {
	ID       string            `json:"id"`
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
	Insecure bool              `json:"insecure,omitempty"`
}

func runOTEL(args *docopt.Args) error {
	client, base, err := otelPluginClient()
	if err != nil {
		return err
	}
	switch {
	case args.Bool["add"]:
		return runOTELAdd(client, base, args)
	case args.Bool["remove"]:
		return runOTELRemove(client, base, args.String["<id>"])
	default:
		return runOTELList(client, base)
	}
}

func otelPluginClient() (*http.Client, string, error) {
	ctrl, err := controllerClient()
	if err != nil {
		return nil, "", err
	}
	apps, err := ctrl.AppList()
	if err != nil {
		return nil, "", err
	}
	app, err := lookupOTELPlugin(apps)
	if err != nil {
		return nil, "", err
	}
	return discoverdHTTPClient(), otelPluginBase(app), nil
}

func lookupOTELPlugin(apps []*ct.App) (*ct.App, error) {
	for _, name := range []string{"otel", "opentelemetry"} {
		if app, err := plugin.LookupPluginApp(apps, name); err == nil {
			return app, nil
		}
	}
	return nil, fmt.Errorf("the otel plugin is not installed\nInstall it with: sudo flynn-host plugin install otel")
}

func otelPluginBase(app *ct.App) string {
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
	name := "otel"
	if app != nil && app.Name != "" {
		name = app.Name
	}
	return "http://" + name + ".discoverd"
}

func runOTELList(client *http.Client, base string) error {
	rows, err := otelList(client, base)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ID", "ENDPOINT")
	for _, e := range rows {
		ep := e.Endpoint
		if e.Insecure {
			ep += " (insecure)"
		}
		listRec(w, e.ID, ep)
	}
	return nil
}

func runOTELAdd(client *http.Client, base string, args *docopt.Args) error {
	endpoint := strings.TrimRight(strings.TrimSpace(args.String["<endpoint>"]), "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid OTLP endpoint %q (want http://host:4318 or https://…)", args.String["<endpoint>"])
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("invalid OTLP endpoint scheme %q (want http or https)", u.Scheme)
	}
	headers, err := parseOTELHeaders(cliutil.List(args, "--header"))
	if err != nil {
		return err
	}
	created, err := otelCreate(client, base, otelExporter{
		Endpoint: endpoint,
		Headers:  headers,
		Insecure: args.Bool["--insecure"],
	})
	if err != nil {
		return err
	}
	log.Printf("Created OpenTelemetry exporter %s -> %s.", created.ID, created.Endpoint)
	return nil
}

func runOTELRemove(client *http.Client, base, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("exporter id is required")
	}
	if err := otelDelete(client, base, id); err != nil {
		return err
	}
	log.Printf("Removed OpenTelemetry exporter %s.", id)
	return nil
}

func parseOTELHeaders(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, h := range raw {
		name, val, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid --header %q (want Name: value)", h)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(val)
	}
	return out, nil
}

func otelList(client *http.Client, base string) ([]otelExporter, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/exporters", nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("otel plugin: HTTP %d %s", res.StatusCode, bytes.TrimSpace(body))
	}
	var rows []otelExporter
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func otelCreate(client *http.Client, base string, exp otelExporter) (*otelExporter, error) {
	raw, err := json.Marshal(exp)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/exporters", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("otel plugin: HTTP %d %s", res.StatusCode, bytes.TrimSpace(body))
	}
	var created otelExporter
	if err := json.Unmarshal(body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func otelDelete(client *http.Client, base, id string) error {
	req, err := http.NewRequest(http.MethodDelete, base+"/exporters/"+id, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("otel plugin: HTTP %d %s", res.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

package cli

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/cliutil"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("otel", runOTEL, `
usage: flynn-host otel
       flynn-host otel add [--logs] [--metrics] [--scope <scope>] [--app <app>] [--header <header>]... [--insecure] <endpoint>
       flynn-host otel remove <id>

Forward Flynn cluster metrics and/or system logs to an OpenTelemetry collector
(OTLP/HTTP JSON, Grafana Alloy, the collector, etc.).

Logs:
    flynn-host otel add --scope system   Flynn system jobs (controller, router, plugins, …)
    flynn-host otel add --scope apps     user application jobs
    flynn-host otel add --scope all      everything (default when --scope is omitted)
    flynn-host otel add --app NAME       one app (same as flynn -a NAME logsink)

App logs can also use flynn logsink (per app) or flynn-host log-sink
--scope apps (all user apps). Those commands stay syslog; this command is OTLP.

Options:
    --logs             Forward logs (default: logs and metrics when neither flag is set)
    --metrics          Forward host and job metrics
    --scope=<scope>    system, apps, or all [default: all]
    --app=<app>        Limit logs to one app name or ID
    --header=<header>  Extra HTTP header "Name: value" (repeatable)
    --insecure         Skip TLS verification of the collector

Examples:

    $ flynn-host otel add --scope system http://alloy.example:4318
    $ flynn-host otel add --logs --scope apps --app myapp https://otlp.example:4318
    $ flynn-host otel add --metrics --header "Authorization: Bearer TOKEN" https://otlp.grafana.net/otlp
`)
}

func runOTEL(args *docopt.Args) error {
	switch {
	case args.Bool["add"]:
		return runOTELAdd(args)
	case args.Bool["remove"]:
		return runHostLogSinkRemove(args)
	default:
		return runOTELList()
	}
}

func runOTELList() error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	sinks, err := client.ListSinks()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ID", "KIND", "APP", "CONFIG")
	for _, j := range sinks {
		if j.Kind != ct.SinkKindOTLP {
			continue
		}
		var config string
		if j.Config != nil {
			config = string(*j.Config)
		}
		app := j.AppID
		if app == "" {
			app = "(cluster)"
		}
		listRec(w, j.ID, j.Kind, app, config)
	}
	return nil
}

func runOTELAdd(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
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
	scope, err := ct.ParseSinkScope(args.String["--scope"])
	if err != nil {
		return err
	}
	logs := args.Bool["--logs"]
	metrics := args.Bool["--metrics"]
	if !logs && !metrics {
		logs, metrics = true, true
	}
	appID, err := resolveSinkAppID(client, args.String["--app"])
	if err != nil {
		return err
	}
	headers, err := parseOTELHeaders(cliutil.List(args, "--header"))
	if err != nil {
		return err
	}
	data, _ := json.Marshal(ct.OTLPSinkConfig{
		Endpoint: endpoint,
		Headers:  headers,
		Insecure: args.Bool["--insecure"],
		Logs:     logs,
		Metrics:  metrics,
		Scope:    scope,
	})
	config := json.RawMessage(data)
	sink := &ct.Sink{
		Kind:   ct.SinkKindOTLP,
		AppID:  appID,
		Config: &config,
	}
	if err := client.CreateSink(sink); err != nil {
		return err
	}
	log.Printf("Created OpenTelemetry sink %s (logs=%v metrics=%v scope=%s).", sink.ID, logs, metrics, scope)
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

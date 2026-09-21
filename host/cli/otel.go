package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func init() {
	Register("otel", runOTELListCmd, `
usage: flynn-host otel

List OpenTelemetry exporters.

Requires the otel plugin:

    sudo flynn-host plugin:install otel

The plugin polls GET /cluster/stats and GET /cluster/jobs-stats and POSTs
/v1/metrics. Job logs stay on flynn-host log-sink / flynn log-sink (syslog).
`)
	Register("otel:add", runOTELAddCmd, `
usage: flynn-host otel:add [--auth <type>] [--token <token>] [--username <user>] [--password <pass>] [--header <header>]... [--insecure] <endpoint>

Forward Flynn cluster metrics to an OpenTelemetry collector (OTLP/HTTP JSON).

Options:
    --auth=<type>      Auth type: none, bearer, or basic (default: none)
    --token=<token>    Bearer token
    --username=<user>  Basic auth username
    --password=<pass>  Basic auth password
    --header=<header>  Extra HTTP header "Name: value" (repeatable)
    --insecure         Skip TLS verification of the collector

Examples:

    $ flynn-host otel:add http://alloy.example:4318
    $ flynn-host otel:add --auth bearer --token TOKEN https://otlp.grafana.net/otlp
    $ flynn-host otel:add --auth basic --username USER --password PASS https://collector.example:4318
    $ flynn-host otel:add --insecure https://collector.example:4318
`)
	Register("otel:remove", runOTELRemoveCmd, `
usage: flynn-host otel:remove <id>

Remove an OpenTelemetry exporter.
`)
}

func runOTELListCmd(_ *docopt.Args) error {
	client, base, key, err := otelPluginClient()
	if err != nil {
		return err
	}
	return runOTELList(client, base, key)
}

func runOTELAddCmd(args *docopt.Args) error {
	client, base, key, err := otelPluginClient()
	if err != nil {
		return err
	}
	return runOTELAdd(client, base, key, args)
}

func runOTELRemoveCmd(args *docopt.Args) error {
	client, base, key, err := otelPluginClient()
	if err != nil {
		return err
	}
	return runOTELRemove(client, base, key, args.String["<id>"])
}

type otelExporter struct {
	ID       string            `json:"id"`
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
	Insecure bool              `json:"insecure,omitempty"`
}

func otelPluginClient() (*http.Client, string, string, error) {
	ctrl, err := controllerClient()
	if err != nil {
		return nil, "", "", err
	}
	apps, err := ctrl.AppList()
	if err != nil {
		return nil, "", "", err
	}
	app, err := lookupOTELPlugin(apps)
	if err != nil {
		return nil, "", "", err
	}
	key, err := otelClusterKey()
	if err != nil {
		return nil, "", "", err
	}
	return discoverdHTTPClient(), otelPluginBase(app), key, nil
}

// otelClusterKey is the Flynn API key for the otel plugin /exporters routes
// (not collector --auth headers). Prefer CONTROLLER_KEY / AUTH_KEY so this
// still works if discoverd stops publishing AUTH_KEY (SEC-028).
func otelClusterKey() (string, error) {
	if key := strings.TrimSpace(os.Getenv("CONTROLLER_KEY")); key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(os.Getenv("AUTH_KEY")); key != "" {
		return key, nil
	}
	return controllerAuthKey()
}

func otelSetPluginAuth(req *http.Request, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("otel plugin: cluster key is required")
	}
	req.SetBasicAuth("", key)
	return nil
}

func lookupOTELPlugin(apps []*ct.App) (*ct.App, error) {
	for _, name := range []string{"otel", "opentelemetry"} {
		if app, err := plugin.LookupPluginApp(apps, name); err == nil {
			return app, nil
		}
	}
	return nil, fmt.Errorf("the otel plugin is not installed\nInstall it with: sudo flynn-host plugin:install otel")
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

func runOTELList(client *http.Client, base, key string) error {
	rows, err := otelList(client, base, key)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ID", "ENDPOINT", "AUTH")
	for _, e := range rows {
		ep := e.Endpoint
		if e.Insecure {
			ep += " (insecure)"
		}
		listRec(w, e.ID, ep, otelAuthKind(e.Headers))
	}
	return nil
}

func runOTELAdd(client *http.Client, base, key string, args *docopt.Args) error {
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
	authHeaders, err := otelAuthHeaders(args)
	if err != nil {
		return err
	}
	if len(authHeaders) > 0 {
		if headers == nil {
			headers = map[string]string{}
		}
		for k, v := range authHeaders {
			headers[k] = v
		}
	}
	created, err := otelCreate(client, base, key, otelExporter{
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

func runOTELRemove(client *http.Client, base, key, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("exporter id is required")
	}
	if err := otelDelete(client, base, key, id); err != nil {
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

func otelAuthHeaders(args *docopt.Args) (map[string]string, error) {
	auth := strings.ToLower(strings.TrimSpace(args.String["--auth"]))
	token := strings.TrimSpace(args.String["--token"])
	user := strings.TrimSpace(args.String["--username"])
	pass := args.String["--password"]
	if auth == "" {
		switch {
		case token != "":
			auth = "bearer"
		case user != "" || pass != "":
			auth = "basic"
		default:
			return nil, nil
		}
	}
	switch auth {
	case "none":
		if token != "" || user != "" || pass != "" {
			return nil, fmt.Errorf("--auth none cannot be combined with --token or --username/--password")
		}
		return nil, nil
	case "bearer":
		if token == "" {
			return nil, fmt.Errorf("--token is required for bearer auth")
		}
		if user != "" || pass != "" {
			return nil, fmt.Errorf("bearer auth cannot be combined with --username/--password")
		}
		if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
			token = "Bearer " + token
		}
		return map[string]string{"Authorization": token}, nil
	case "basic":
		if user == "" || pass == "" {
			return nil, fmt.Errorf("--username and --password are required for basic auth")
		}
		if token != "" {
			return nil, fmt.Errorf("basic auth cannot be combined with --token")
		}
		raw := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		return map[string]string{"Authorization": "Basic " + raw}, nil
	default:
		return nil, fmt.Errorf("unknown --auth %q (want none, bearer, or basic)", args.String["--auth"])
	}
}

func otelAuthKind(headers map[string]string) string {
	if len(headers) == 0 {
		return "none"
	}
	auth := headers["Authorization"]
	if auth == "" {
		for k, v := range headers {
			if strings.EqualFold(k, "Authorization") {
				auth = v
				break
			}
		}
	}
	switch {
	case strings.HasPrefix(strings.ToLower(auth), "bearer "):
		return "bearer"
	case strings.HasPrefix(strings.ToLower(auth), "basic "):
		return "basic"
	case len(headers) > 0:
		return "headers"
	default:
		return "none"
	}
}

func otelList(client *http.Client, base, key string) ([]otelExporter, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/exporters", nil)
	if err != nil {
		return nil, err
	}
	if err := otelSetPluginAuth(req, key); err != nil {
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

func otelCreate(client *http.Client, base, key string, exp otelExporter) (*otelExporter, error) {
	raw, err := json.Marshal(exp)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/exporters", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := otelSetPluginAuth(req, key); err != nil {
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
	var created otelExporter
	if err := json.Unmarshal(body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func otelDelete(client *http.Client, base, key, id string) error {
	req, err := http.NewRequest(http.MethodDelete, base+"/exporters/"+id, nil)
	if err != nil {
		return err
	}
	if err := otelSetPluginAuth(req, key); err != nil {
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

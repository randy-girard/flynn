package cli

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("log-sink", runClusterLogSinkListCmd, `
usage: flynn-host log-sink

List cluster log sinks.

OpenTelemetry metrics use the otel plugin (flynn-host plugin:install otel,
then flynn-host otel).
`)
	Register("log-sink:list", runLogSinkListCmd, `
usage: flynn-host log-sink:list [<host>]

List cluster log sinks, or sinks configured on a specific host.

Examples:

    $ flynn-host log-sink:list
    $ flynn-host log-sink:list host1
`)
	Register("log-sink:add", runHostLogSinkAddSyslog, `
usage: flynn-host log-sink:add syslog [--scope <scope>] [--app <app>] [--use-ids] [--insecure] [--format <format>] <url> [<prefix>]

Create a cluster-wide syslog sink.

Options:
	--scope=<scope>    system (Flynn jobs), apps (user apps), or all [default: all]
	--app=<app>        Limit the sink to one app name or ID
	--use-ids          Use app IDs instead of app names in the syslog APP-NAME field.
	--insecure         Don't verify servers certificate chain or hostname. Should only be used for testing.
	--format=<format>  One of rfc6587, newline, or prefixed_newline. Defaults to rfc6587.

System logs (controller, router, plugins, other flynn-system-app jobs) use
--scope system. User app logs use --scope apps, or --app NAME for one app
(same as flynn -a NAME log-sink).

Examples:

    $ flynn-host log-sink:add syslog syslog+tls://rsyslog.host:514/
    $ flynn-host log-sink:add syslog --scope system syslog://logs.example:514/
    $ flynn-host log-sink:add syslog --app myapp syslog://logs.example:514/
`)
	Register("log-sink:remove", runHostLogSinkRemove, `
usage: flynn-host log-sink:remove <id>

Remove a cluster log sink.
`)
}

func runClusterLogSinkListCmd() error {
	return runClusterLogSinkList()
}

func runLogSinkListCmd(args *docopt.Args, client *cluster.Client) error {
	if args.String["<host>"] == "" {
		return runClusterLogSinkList()
	}
	return runLogSinkList(args, client)
}

func runLogSinkList(args *docopt.Args, client *cluster.Client) error {
	hostClient, err := client.Host(args.String["<host>"])
	if err != nil {
		return fmt.Errorf("could not connect to host: %s", err)
	}
	sinks, err := hostClient.GetSinks()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w,
		"ID",
		"KIND",
		"CONFIG",
		"HOST MANAGED",
	)

	for _, sink := range sinks {
		var config string
		if sink.Config != nil {
			config = string(*sink.Config)
		}
		listRec(w,
			sink.ID,
			sink.Kind,
			string(config),
			sink.HostManaged,
		)
	}
	return nil
}

func runClusterLogSinkList() error {
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

func runHostLogSinkAddSyslog(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	u, err := url.Parse(args.String["<url>"])
	if err != nil {
		return fmt.Errorf("Invalid syslog URL: %s", err)
	}
	switch u.Scheme {
	case "syslog", "syslog+tls":
	default:
		return fmt.Errorf("Invalid syslog protocol: %s", u.Scheme)
	}

	var format ct.SyslogFormat
	switch args.String["--format"] {
	case "newline":
		format = ct.SyslogFormatNewline
	case "prefixed_newline":
		format = ct.SyslogFormatPrefixedNewline
	case "rfc6587", "":
		format = ct.SyslogFormatRFC6587
	default:
		return fmt.Errorf("Invalid syslog format: %s", args.String["--format"])
	}

	scope, err := ct.ParseSinkScope(args.String["--scope"])
	if err != nil {
		return err
	}
	appID, err := resolveSinkAppID(client, args.String["--app"])
	if err != nil {
		return err
	}

	data, _ := json.Marshal(ct.SyslogSinkConfig{
		Prefix:   args.String["<prefix>"],
		URL:      u.String(),
		UseIDs:   args.Bool["--use-ids"],
		Insecure: args.Bool["--insecure"],
		Format:   format,
		Scope:    scope,
	})
	config := json.RawMessage(data)
	sink := &ct.Sink{
		Kind:   ct.SinkKindSyslog,
		AppID:  appID,
		Config: &config,
	}
	if err := client.CreateSink(sink); err != nil {
		return err
	}
	log.Printf("Created sink %s.", sink.ID)
	return nil
}

func runHostLogSinkRemove(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	id := args.String["<id>"]
	res, err := client.DeleteSink(id)
	if err != nil {
		return err
	}
	log.Printf("Deleted sink %s.", res.ID)
	return nil
}

func resolveSinkAppID(client controller.Client, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	app, err := client.GetApp(name)
	if err != nil {
		return "", fmt.Errorf("app %q: %w", name, err)
	}
	return app.ID, nil
}

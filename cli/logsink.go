package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("log-sink", runAppLogSinkList, `
usage: flynn log-sink

List log sinks for the app.
`)
	register("log-sink:add", runAppLogSinkAdd, `
usage: flynn log-sink:add syslog [--use-ids] [--insecure] [--format <format>] <url> [<prefix>]

Add a syslog log sink for the app. Supported schemes are syslog and syslog+tls.
Cluster-wide and Flynn system logs use flynn-host log-sink. Cluster metrics
use the otel plugin (flynn-host otel).

Options:
	--use-ids          Use app IDs instead of app names in the syslog APP-NAME field.
	--insecure         Don't verify servers certificate chain or hostname. Should only be used for testing.
	--format=<format>  One of rfc6587, newline, or prefixed_newline. Defaults to rfc6587.

Examples:

	$ flynn log-sink:add syslog syslog+tls://rsyslog.host:514/
`)
	register("log-sink:remove", runAppLogSinkRemove, `
usage: flynn log-sink:remove <id>

Remove an app log sink with <id>.
`)
}

func runAppLogSinkList(_ *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	sinks, err := client.ListSinks()
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()
	listRec(w, "ID", "KIND", "CONFIG")
	for _, j := range sinks {
		if j.AppID != app.ID {
			continue
		}
		var config string
		if j.Config != nil {
			config = string(*j.Config)
		}
		listRec(w, j.ID, j.Kind, config)
	}
	return nil
}

func runAppLogSinkAdd(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
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

	data, _ := json.Marshal(ct.SyslogSinkConfig{
		Prefix:   args.String["<prefix>"],
		URL:      u.String(),
		UseIDs:   args.Bool["--use-ids"],
		Insecure: args.Bool["--insecure"],
		Format:   format,
	})
	config := json.RawMessage(data)
	sink := &ct.Sink{
		Kind:   ct.SinkKindSyslog,
		AppID:  app.ID,
		Config: &config,
	}
	if err := client.CreateSink(sink); err != nil {
		return err
	}
	log.Printf("Created sink %s.", sink.ID)
	return nil
}

func runAppLogSinkRemove(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	id := args.String["<id>"]
	sink, err := client.GetSink(id)
	if err != nil {
		return err
	}
	if sink.AppID == "" {
		return fmt.Errorf("sink %s is a cluster log sink; remove it with flynn-host log-sink:remove", id)
	}
	if sink.AppID != app.ID {
		return fmt.Errorf("sink %s does not belong to this app", id)
	}
	res, err := client.DeleteSink(id)
	if err != nil {
		return err
	}
	log.Printf("Deleted sink %s.", res.ID)
	return nil
}

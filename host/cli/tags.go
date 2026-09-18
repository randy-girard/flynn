package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("tags", runTagsList, `
usage: flynn-host tags

List flynn-host daemon tags.
`)
	Register("tags:set", runTagsSet, `
usage: flynn-host tags:set <hostid> <var>=<val>...

Set one or more flynn-host daemon tags.
`)
	Register("tags:del", runTagsDel, `
usage: flynn-host tags:del <hostid> <var>...

Delete one or more flynn-host daemon tags.
`)
}

func runTagsList(_ *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "HOST", "TAGS")
	for _, host := range hosts {
		tags := make([]string, 0, len(host.Tags()))
		for k, v := range host.Tags() {
			tags = append(tags, fmt.Sprintf("%s=%s", k, v))
		}
		listRec(w, host.ID(), strings.Join(tags, " "))
	}
	return nil
}

func runTagsSet(args *docopt.Args, client *cluster.Client) error {
	host, err := client.Host(args.String["<hostid>"])
	if err != nil {
		return err
	}
	pairs := cliutil.List(args, "<var>=<val>")
	tags := make(map[string]string, len(pairs))
	for _, s := range pairs {
		keyVal := strings.SplitN(s, "=", 2)
		if len(keyVal) == 1 && keyVal[0] != "" {
			tags[keyVal[0]] = "true"
		} else if len(keyVal) == 2 {
			tags[keyVal[0]] = keyVal[1]
		}
	}
	return host.UpdateTags(tags)
}

func runTagsDel(args *docopt.Args, client *cluster.Client) error {
	host, err := client.Host(args.String["<hostid>"])
	if err != nil {
		return err
	}
	vars := cliutil.List(args, "<var>")
	tags := make(map[string]string, len(vars))
	for _, v := range vars {
		// empty tags get deleted on the host
		tags[v] = ""
	}
	return host.UpdateTags(tags)
}

package cli

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("tags", runTagsList, `
usage: flynn-host tags

List flynn-host daemon tags and the optional flynn-max-processes cap.
`)
	Register("tags:set", runTagsSet, `
usage: flynn-host tags:set <hostid> <var>=<val>...

Set one or more flynn-host daemon tags. Use flynn-max-processes=N (or
tags:max-processes) to cap tenant jobs on that node.
`)
	Register("tags:del", runTagsDel, `
usage: flynn-host tags:del <hostid> <var>...

Delete one or more flynn-host daemon tags.
`)
	Register("tags:max-processes", runTagsMaxProcesses, `
usage: flynn-host tags:max-processes <hostid> [<n>]

Show or set how many tenant jobs (user apps and datastore instances) may
run on this node. Omni and other system jobs are not counted. "unlimited"
or 0 removes the cap.
`)
}

func runTagsList(_ *docopt.Args, client *cluster.Client) error {
	hosts, err := client.Hosts()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "HOST", "MAX", "TAGS")
	for _, host := range hosts {
		tags := make([]string, 0, len(host.Tags()))
		for k, v := range host.Tags() {
			if k == ct.FormationMaxProcessesTag {
				continue
			}
			tags = append(tags, fmt.Sprintf("%s=%s", k, v))
		}
		max := "unlimited"
		if n, ok := ct.ParseHostMaxProcesses(host.Tags()); ok {
			max = strconv.Itoa(n)
		}
		listRec(w, host.ID(), max, strings.Join(tags, " "))
	}
	return nil
}

func parseTagPairs(pairs []string) map[string]string {
	tags := make(map[string]string, len(pairs))
	for _, s := range pairs {
		keyVal := strings.SplitN(s, "=", 2)
		if len(keyVal) == 1 && keyVal[0] != "" {
			tags[keyVal[0]] = "true"
		} else if len(keyVal) == 2 && keyVal[0] != "" {
			tags[keyVal[0]] = keyVal[1]
		}
	}
	return tags
}

// formatDaemonTagsArg builds the flynn-host daemon --tags value written by init
// into host.json. --max-processes wins over a flynn-max-processes --tag.
func formatDaemonTagsArg(tagPairs []string, maxProcesses string) (string, error) {
	tags := parseTagPairs(tagPairs)
	if err := applyInitMaxProcesses(tags, maxProcesses); err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := tags[k]
		if v == "" || v == "true" {
			parts = append(parts, k)
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ","), nil
}

func applyInitMaxProcesses(tags map[string]string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	switch strings.ToLower(raw) {
	case "unlimited", "off", "none", "0":
		delete(tags, ct.FormationMaxProcessesTag)
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fmt.Errorf("invalid --max-processes %q", raw)
	}
	tags[ct.FormationMaxProcessesTag] = ct.EncodeHostMaxProcesses(n)
	return nil
}

func runTagsSet(args *docopt.Args, client *cluster.Client) error {
	host, err := client.Host(args.String["<hostid>"])
	if err != nil {
		return err
	}
	pairs := cliutil.List(args, "<var>=<val>")
	return host.UpdateTags(parseTagPairs(pairs))
}

func runTagsMaxProcesses(args *docopt.Args, client *cluster.Client) error {
	host, err := client.Host(args.String["<hostid>"])
	if err != nil {
		return err
	}
	arg := strings.TrimSpace(args.String["<n>"])
	if arg == "" {
		if n, ok := ct.ParseHostMaxProcesses(host.Tags()); ok {
			fmt.Printf("max_processes=%d\n", n)
			return nil
		}
		fmt.Println("max_processes=unlimited")
		return nil
	}
	var n int
	switch strings.ToLower(arg) {
	case "unlimited", "off", "none", "0":
		n = 0
	default:
		n, err = strconv.Atoi(arg)
		if err != nil || n < 1 {
			return fmt.Errorf("max processes must be a positive number or \"unlimited\", got %q", arg)
		}
	}
	return host.UpdateTags(map[string]string{ct.FormationMaxProcessesTag: ct.EncodeHostMaxProcesses(n)})
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

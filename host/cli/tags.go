package cli

import (
	"fmt"
	"os"
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

List flynn-host daemon tags, the optional flynn-max-processes cap, and
any flynn-max-persistent-singletons cap in the tag list.
`)
	Register("tags:set", runTagsSet, `
usage: flynn-host tags:set <hostid> <var>=<val>...

Set one or more flynn-host daemon tags. Use flynn-max-processes=N (or
tags:max-processes) to cap tenant jobs on that node. Use
flynn-max-persistent-singletons=N to cap new single-node databases on
that node. unlimited clears it. The value is written to host.json.
`)
	Register("tags:del", runTagsDel, `
usage: flynn-host tags:del <hostid> <var>...

Delete one or more flynn-host daemon tags.
`)
	Register("tags:max-processes", runTagsMaxProcesses, `
usage: flynn-host tags:max-processes <hostid> [<n>] [--auto]

Show or set how many tenant jobs (user apps and datastore instances) may
run on this node. Omni and other system jobs are not counted. "unlimited"
or 0 removes the cap and is not refreshed.

--auto sets the cap from this host's memory and the current app and
database runtime sizes, then the host daemon keeps that number aligned
when those sizes change. A number is an operator cap and stays put.
Pass a number or --auto, not both.

Options:
  --auto   derive the cap from host memory and published runtimes
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
			if k == ct.FormationMaxProcessesTag || k == ct.FormationMaxProcessesSourceTag {
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
// existing is the tag map already stored in host.json. Nil means the file
// does not exist yet. A non-nil map with no cap stays unlimited.
func formatDaemonTagsArg(tagPairs []string, maxProcesses string, existing map[string]string) (string, error) {
	tags := parseTagPairs(tagPairs)
	if err := applyInitMaxProcesses(tags, maxProcesses, existing); err != nil {
		return "", err
	}
	return formatTagMap(tags), nil
}

func applyInitMaxProcesses(tags map[string]string, raw string, existing map[string]string) error {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		switch strings.ToLower(raw) {
		case "unlimited", "off", "none", "0":
			delete(tags, ct.FormationMaxProcessesTag)
			delete(tags, ct.FormationMaxProcessesSourceTag)
			return nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid --max-processes %q", raw)
		}
		tags[ct.FormationMaxProcessesTag] = ct.EncodeHostMaxProcesses(n)
		tags[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceOperator
		return nil
	}
	if _, ok := tags[ct.FormationMaxProcessesTag]; ok {
		return classifyExplicitMaxProcessesTag(tags)
	}
	if existing != nil {
		if v, ok := existing[ct.FormationMaxProcessesTag]; ok {
			tags[ct.FormationMaxProcessesTag] = v
		}
		if v, ok := existing[ct.FormationMaxProcessesSourceTag]; ok {
			tags[ct.FormationMaxProcessesSourceTag] = v
		}
		return nil
	}
	tags[ct.FormationMaxProcessesTag] = ct.HostMaxProcessesAuto
	tags[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceAuto
	return nil
}

func classifyExplicitMaxProcessesTag(tags map[string]string) error {
	raw := strings.ToLower(strings.TrimSpace(tags[ct.FormationMaxProcessesTag]))
	switch raw {
	case "", "unlimited", "off", "none", "0":
		delete(tags, ct.FormationMaxProcessesTag)
		delete(tags, ct.FormationMaxProcessesSourceTag)
		return nil
	case ct.HostMaxProcessesAuto:
		tags[ct.FormationMaxProcessesTag] = ct.HostMaxProcessesAuto
		tags[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceAuto
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fmt.Errorf("invalid --tag %s=%q", ct.FormationMaxProcessesTag, tags[ct.FormationMaxProcessesTag])
	}
	tags[ct.FormationMaxProcessesTag] = ct.EncodeHostMaxProcesses(n)
	tags[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceOperator
	return nil
}

func runTagsSet(args *docopt.Args, client *cluster.Client) error {
	host, err := client.Host(args.String["<hostid>"])
	if err != nil {
		return err
	}
	pairs := cliutil.List(args, "<var>=<val>")
	tags := parseTagPairs(pairs)
	if err := classifySetMaxProcesses(tags); err != nil {
		return err
	}
	classifySetSingletonCap(tags)
	return host.UpdateTags(tags)
}

// classifySetSingletonCap turns a sentinel into a delete. A number is stored
// as typed. A non-integer is left so production:check can show it.
func classifySetSingletonCap(tags map[string]string) {
	raw, ok := tags[ct.FormationMaxPersistentSingletonsTag]
	if !ok {
		return
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "unlimited", "off", "none", "0", "auto":
		tags[ct.FormationMaxPersistentSingletonsTag] = ""
	default:
		tags[ct.FormationMaxPersistentSingletonsTag] = strings.TrimSpace(raw)
	}
}

// classifySetMaxProcesses marks an explicit cap as operator so the daemon
// does not refresh it. Clearing the cap clears the source too.
func classifySetMaxProcesses(tags map[string]string) error {
	raw, ok := tags[ct.FormationMaxProcessesTag]
	if !ok {
		return nil
	}
	if _, set := tags[ct.FormationMaxProcessesSourceTag]; set {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "unlimited", "off", "none", "0":
		tags[ct.FormationMaxProcessesTag] = ""
		tags[ct.FormationMaxProcessesSourceTag] = ""
		return nil
	case ct.HostMaxProcessesAuto:
		tags[ct.FormationMaxProcessesTag] = ct.HostMaxProcessesAuto
		tags[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceAuto
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return fmt.Errorf("invalid %s=%q", ct.FormationMaxProcessesTag, raw)
	}
	tags[ct.FormationMaxProcessesTag] = ct.EncodeHostMaxProcesses(n)
	tags[ct.FormationMaxProcessesSourceTag] = ct.HostMaxProcessesSourceOperator
	return nil
}

func runTagsMaxProcesses(args *docopt.Args, client *cluster.Client) error {
	host, err := client.Host(args.String["<hostid>"])
	if err != nil {
		return err
	}
	if args.Bool["--auto"] {
		if strings.TrimSpace(args.String["<n>"]) != "" {
			return fmt.Errorf("pass a number or --auto, not both")
		}
		return setAutoMaxProcesses(host)
	}
	arg := strings.TrimSpace(args.String["<n>"])
	if arg == "" {
		if n, ok := ct.ParseHostMaxProcesses(host.Tags()); ok {
			fmt.Printf("max_processes=%d\n", n)
		} else {
			fmt.Println("max_processes=unlimited")
		}
		if src := strings.TrimSpace(host.Tags()[ct.FormationMaxProcessesSourceTag]); src != "" {
			fmt.Printf("source=%s\n", src)
		}
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
	return host.UpdateTags(map[string]string{
		ct.FormationMaxProcessesTag:       ct.EncodeHostMaxProcesses(n),
		ct.FormationMaxProcessesSourceTag: sourceForCap(n),
	})
}

func sourceForCap(n int) string {
	if n < 1 {
		return ""
	}
	return ct.HostMaxProcessesSourceOperator
}

func setAutoMaxProcesses(h *cluster.Host) error {
	stats, err := h.GetStats()
	if err != nil {
		return err
	}
	if stats == nil || stats.MemoryTotalBytes == 0 {
		return fmt.Errorf("host memory is unknown")
	}
	overlay := ct.DefaultOverlayContainerAddrs()
	if status, err := h.GetStatus(); err == nil && status != nil && status.Network != nil {
		if n, ok := ct.OverlayContainerAddrsFromCIDR(status.Network.Subnet); ok {
			overlay = n
		}
	}
	ctrl, err := controllerClient()
	if err != nil {
		return err
	}
	profiles, err := ctrl.ListRuntimeProfiles()
	if err != nil {
		return err
	}
	cat, err := ctrl.ListDBRuntimes()
	if err != nil {
		return err
	}
	n := ct.DefaultHostMaxProcesses(int64(stats.MemoryTotalBytes), RuntimeMemoryLimits(profiles, cat), overlay)
	if n < 1 {
		return fmt.Errorf("cannot compute a process cap from the current runtimes")
	}
	return h.UpdateTags(map[string]string{
		ct.FormationMaxProcessesTag:       ct.EncodeHostMaxProcesses(n),
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	})
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
		if v == ct.FormationMaxProcessesTag {
			tags[ct.FormationMaxProcessesSourceTag] = ""
		}
	}
	return host.UpdateTags(tags)
}

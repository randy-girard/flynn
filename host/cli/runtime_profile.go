package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

const (
	runtimeListUsage = `
usage: flynn-host runtime

List cluster runtimes (CPU/memory presets).
`
	runtimeCreateUsage = `
usage: flynn-host runtime:create [--memory <bytes>] [--cpu <milli>] [--reserve] [--tag <tag>]... <name>

Create a runtime. Memory is bytes (or 512MB / 1GB). CPU is milliCPU.
Without --reserve the runtime shares host capacity and only the CPU/memory
maxes apply. --reserve guarantees that much Request on the host.
--tag KEY=VAL (repeatable) pins jobs using this runtime to matching hosts.

Options:
    --memory=<bytes>  Memory cap (default 512MB)
    --cpu=<milli>     milliCPU cap (default 500)
    --reserve         Guarantee CPU and memory when placing jobs (off by default)
    --tag=<tag>       Host placement tag (KEY or KEY=VAL)

Examples:

    $ flynn-host runtime:create --memory 512MB --cpu 500 xlarge
    $ flynn-host runtime:create --reserve --memory 1GB --cpu 1000 isolated
    $ flynn-host runtime:create --tag role=database dedicated
`
	runtimeUpdateUsage = `
usage: flynn-host runtime:update [--name <name>] [--memory <bytes>] [--cpu <milli>] [--reserve] [--shared] [--tag <tag>]... <id>

Update a runtime by id, including builtin small/medium/large.
--reserve guarantees CPU/memory; --shared turns the guarantee off (default for new runtimes).
--tag KEY=VAL replaces placement tags when given.
`
	runtimeRemoveUsage = `
usage: flynn-host runtime:remove <id>

Delete a custom runtime. Builtin small/medium/large cannot be removed.
`
	runtimeAllowCustomUsage = `
usage: flynn-host runtime:allow-custom [--disable]

Allow (or --disable) cluster admins and app operators to set raw CPU/memory
limits instead of only named runtimes.
`
	runtimeReserveUsage = `
usage: flynn-host runtime:reserve [--disable] <id>

Guarantee this runtime's CPU and memory when placing jobs (off by default).
Shared runtimes keep the same CPU/memory maxes but do not hold a reservation.
`
	runtimeMaxRestartsUsage = `
usage: flynn-host runtime:max-restarts [<count>]

Show or set how many times in a row the scheduler restarts an app process
that crashes before leaving it stopped. <count> is a number of restarts or
"unlimited" (the default). A process that runs for 5 minutes resets its count.
Scaling the process type, deploying a new release, or changing this setting
starts stopped processes again. System apps always restart.

Examples:

    $ flynn-host runtime:max-restarts 10
    $ flynn-host runtime:max-restarts unlimited
`
	runtimeJobRestartIntervalUsage = `
usage: flynn-host runtime:job-restart-interval [<interval>]

Show or set how long a job may run before the scheduler restarts it to
release leaked memory. <interval> is a Go duration of at least 1h (default
24h) or "off" (also "0") to disable. Jobs are restarted one at a time per
app, and only while the app is at its full process count. Only user apps
(web, worker, other) are restarted; system apps, plugins and datastores
are never restarted by this rule. Process types with a single instance are
skipped unless runtime:single-instance-restart is on.

Examples:

    $ flynn-host runtime:job-restart-interval 72h
    $ flynn-host runtime:job-restart-interval off
`
	runtimeAllowUserScaleUsage = `
usage: flynn-host runtime:allow-user-scale [--disable]

Allow (or --disable) tenants to change process counts with flynn scale.
Cluster admins and flynn-host are never blocked. Default is on.
`
	runtimeAllowUserScaleTagsUsage = `
usage: flynn-host runtime:allow-user-scale-tags [--disable]

Allow (or --disable) tenants to set formation tags on flynn scale
(for example web=1,role=database). Default is off: tenants pick a
tagged runtime instead of writing host constraints themselves.
`
	runtimeSingleInstanceRestartUsage = `
usage: flynn-host runtime:single-instance-restart [<state>]

Show or set whether the periodic job restart (runtime:job-restart-interval)
also restarts a user app process type that has only one instance. <state>
is "on" or "off" (default off). With it on, that single job is stopped
before its replacement starts, so the app has brief downtime. Datastores
and system apps are never restarted.

Examples:

    $ flynn-host runtime:single-instance-restart on
    $ flynn-host runtime:single-instance-restart off
`
)

func init() {
	Register("runtime", runRuntimeProfileList, runtimeListUsage)
	Register("runtime:create", runRuntimeProfileCreate, runtimeCreateUsage)
	Register("runtime:update", runRuntimeProfileUpdate, runtimeUpdateUsage)
	Register("runtime:remove", runRuntimeProfileRemove, runtimeRemoveUsage)
	Register("runtime:allow-custom", runRuntimeProfileAllowCustom, runtimeAllowCustomUsage)
	Register("runtime:reserve", runRuntimeReserve, runtimeReserveUsage)
	Register("runtime:max-restarts", runRuntimeMaxRestarts, runtimeMaxRestartsUsage)
	Register("runtime:job-restart-interval", runRuntimeJobRestartInterval, runtimeJobRestartIntervalUsage)
	Register("runtime:single-instance-restart", runRuntimeSingleInstanceRestart, runtimeSingleInstanceRestartUsage)
	Register("runtime:allow-user-scale", runRuntimeAllowUserScale, runtimeAllowUserScaleUsage)
	Register("runtime:allow-user-scale-tags", runRuntimeAllowUserScaleTags, runtimeAllowUserScaleTagsUsage)
}

func runtimeSettingsFrom(cur *ct.RuntimeSettings) ct.RuntimeSettings {
	s := cur.Copy()
	s.MaxProcesses = cur.MaxProcessesOrDefault()
	s.BlobGCKeep = cur.BlobGCKeepOrDefault()
	return s
}

// formatMaxRestarts renders RuntimeSettings.MaxRestarts, where 0 is unlimited.
func formatMaxRestarts(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(n)
}

// parseMaxRestarts accepts a restart count or "unlimited" (stored as 0).
func parseMaxRestarts(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "unlimited" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("max restarts must be a positive number or \"unlimited\", got %q", s)
	}
	return n, nil
}

func runRuntimeProfileList(_ *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	list, err := client.ListRuntimeProfiles()
	if err != nil {
		return err
	}
	settings, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	fmt.Printf("allow_custom_limits=%t allow_user_scale=%t allow_user_scale_tags=%t max_processes=%d max_restarts=%s job_restart_interval=%s\n", settings.AllowCustomLimits, settings.AllowUserScale, settings.AllowUserScaleTags, settings.MaxProcessesOrDefault(), formatMaxRestarts(settings.MaxRestarts), formatJobRestartInterval(settings.JobRestartInterval))
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tMEMORY\tCPU\tRESERVE\tTAGS\tBUILTIN")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%t\t%s\t%t\n", p.ID, p.Name, resource.FormatLimit(resource.TypeMemory, p.Memory), p.CPU, p.ReserveResources, formatTags(p.Tags), p.Builtin)
	}
	return w.Flush()
}

func parseMemoryCPU(args *docopt.Args, mem, cpu int64) (int64, int64, error) {
	var err error
	if s := args.String["--memory"]; s != "" {
		mem, err = resource.ParseLimit(resource.TypeMemory, s)
		if err != nil {
			return 0, 0, err
		}
	}
	if s := args.String["--cpu"]; s != "" {
		cpu, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			cpu, err = resource.ParseLimit(resource.TypeCPU, s)
			if err != nil {
				return 0, 0, err
			}
		}
	}
	return mem, cpu, nil
}

func runRuntimeProfileCreate(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	mem, cpu, err := parseMemoryCPU(args, int64(512*1024*1024), int64(500))
	if err != nil {
		return err
	}
	p := &ct.RuntimeProfile{
		Name:             args.String["<name>"],
		Memory:           mem,
		CPU:              cpu,
		ReserveResources: args.Bool["--reserve"],
		Tags:             parseTagPairs(cliutil.List(args, "--tag")),
	}
	if err := client.CreateRuntimeProfile(p); err != nil {
		return err
	}
	fmt.Println(p.ID)
	return nil
}

func lookupHostRuntimeProfile(client interface {
	ListRuntimeProfiles() ([]*ct.RuntimeProfile, error)
	GetRuntimeProfile(id string) (*ct.RuntimeProfile, error)
}, nameOrID string) (*ct.RuntimeProfile, error) {
	if p, err := client.GetRuntimeProfile(nameOrID); err == nil {
		return p, nil
	}
	list, err := client.ListRuntimeProfiles()
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(nameOrID))
	for _, p := range list {
		if p == nil {
			continue
		}
		if p.ID == nameOrID || strings.ToLower(p.Name) == want {
			return p, nil
		}
	}
	return nil, fmt.Errorf("runtime %q not found", nameOrID)
}

func runRuntimeProfileUpdate(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	p, err := lookupHostRuntimeProfile(client, args.String["<id>"])
	if err != nil {
		return err
	}
	if s := args.String["--name"]; s != "" {
		p.Name = s
	}
	p.Memory, p.CPU, err = parseMemoryCPU(args, p.Memory, p.CPU)
	if err != nil {
		return err
	}
	if args.Bool["--reserve"] {
		p.ReserveResources = true
	}
	if args.Bool["--shared"] {
		p.ReserveResources = false
	}
	if tags := parseTagPairs(cliutil.List(args, "--tag")); len(tags) > 0 {
		p.Tags = tags
	}
	if err := client.UpdateRuntimeProfile(p); err != nil {
		return err
	}
	fmt.Println(p.ID)
	return nil
}

func runRuntimeProfileRemove(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	return client.DeleteRuntimeProfile(args.String["<id>"])
}

func runRuntimeProfileAllowCustom(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cur, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	s := runtimeSettingsFrom(cur)
	s.AllowCustomLimits = !args.Bool["--disable"]
	if err := client.UpdateRuntimeSettings(&s); err != nil {
		return err
	}
	fmt.Printf("allow_custom_limits=%t max_processes=%d\n", s.AllowCustomLimits, s.MaxProcessesOrDefault())
	return nil
}

func runRuntimeMaxRestarts(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cur, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	arg := args.String["<count>"]
	if arg == "" {
		fmt.Printf("max_restarts=%s\n", formatMaxRestarts(cur.MaxRestarts))
		return nil
	}
	n, err := parseMaxRestarts(arg)
	if err != nil {
		return err
	}
	s := runtimeSettingsFrom(cur)
	s.MaxRestarts = n
	if err := client.UpdateRuntimeSettings(&s); err != nil {
		return err
	}
	fmt.Printf("max_restarts=%s\n", formatMaxRestarts(s.MaxRestarts))
	return nil
}

// formatJobRestartInterval renders RuntimeSettings.JobRestartInterval, where
// an empty value means the default.
func formatJobRestartInterval(v string) string {
	canon, _, err := ct.ParseJobRestartInterval(v)
	if err != nil {
		return ct.DefaultJobRestartInterval
	}
	return canon
}

func runRuntimeJobRestartInterval(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cur, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	arg := args.String["<interval>"]
	if arg == "" {
		fmt.Printf("job_restart_interval=%s\n", formatJobRestartInterval(cur.JobRestartInterval))
		return nil
	}
	interval, _, err := ct.ParseJobRestartInterval(arg)
	if err != nil {
		return fmt.Errorf("job restart interval: %s", err)
	}
	s := runtimeSettingsFrom(cur)
	s.JobRestartInterval = interval
	if err := client.UpdateRuntimeSettings(&s); err != nil {
		return err
	}
	fmt.Printf("job_restart_interval=%s\n", formatJobRestartInterval(s.JobRestartInterval))
	return nil
}

// runRuntimeSingleInstanceRestart shows or sets the cluster-wide
// single_instance_restart option.
func runRuntimeSingleInstanceRestart(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cur, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	arg := args.String["<on|off>"]
	if arg == "" {
		fmt.Printf("single_instance_restart=%s\n", formatOnOff(cur.SingleInstanceRestart))
		return nil
	}
	on, err := parseOnOff(arg)
	if err != nil {
		return err
	}
	s := runtimeSettingsFrom(cur)
	s.SingleInstanceRestart = on
	if err := client.UpdateRuntimeSettings(&s); err != nil {
		return err
	}
	fmt.Printf("single_instance_restart=%s\n", formatOnOff(s.SingleInstanceRestart))
	return nil
}

func formatOnOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("single instance restart: must be on or off")
}

func runRuntimeReserve(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	p, err := lookupHostRuntimeProfile(client, args.String["<id>"])
	if err != nil {
		return err
	}
	p.ReserveResources = !args.Bool["--disable"]
	if err := client.UpdateRuntimeProfile(p); err != nil {
		return err
	}
	fmt.Printf("%s reserve_resources=%t\n", p.Name, p.ReserveResources)
	return nil
}

func formatTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func runRuntimeAllowUserScale(args *docopt.Args) error {
	return updateRuntimeBoolSetting(args, func(s *ct.RuntimeSettings, on bool) {
		s.AllowUserScale = on
	}, func(s ct.RuntimeSettings) string {
		return fmt.Sprintf("allow_user_scale=%t", s.AllowUserScale)
	})
}

func runRuntimeAllowUserScaleTags(args *docopt.Args) error {
	return updateRuntimeBoolSetting(args, func(s *ct.RuntimeSettings, on bool) {
		s.AllowUserScaleTags = on
	}, func(s ct.RuntimeSettings) string {
		return fmt.Sprintf("allow_user_scale_tags=%t", s.AllowUserScaleTags)
	})
}

func updateRuntimeBoolSetting(args *docopt.Args, set func(*ct.RuntimeSettings, bool), format func(ct.RuntimeSettings) string) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cur, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	s := runtimeSettingsFrom(cur)
	set(&s, !args.Bool["--disable"])
	if err := client.UpdateRuntimeSettings(&s); err != nil {
		return err
	}
	fmt.Println(format(s))
	return nil
}

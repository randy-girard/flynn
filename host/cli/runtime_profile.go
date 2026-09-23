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
)

const (
	runtimeListUsage = `
usage: flynn-host runtime

List cluster runtimes (CPU/memory presets).
`
	runtimeCreateUsage = `
usage: flynn-host runtime:create [--memory <bytes>] [--cpu <milli>] [--reserve] <name>

Create a runtime. Memory is bytes (or 512MB / 1GB). CPU is milliCPU.
Without --reserve the runtime shares host capacity and only the CPU/memory
maxes apply. --reserve guarantees that much Request on the host.

Options:
    --memory=<bytes>  Memory cap (default 512MB)
    --cpu=<milli>     milliCPU cap (default 500)
    --reserve         Guarantee CPU and memory when placing jobs (off by default)

Examples:

    $ flynn-host runtime:create --memory 512MB --cpu 500 xlarge
    $ flynn-host runtime:create --reserve --memory 1GB --cpu 1000 isolated
`
	runtimeUpdateUsage = `
usage: flynn-host runtime:update [--name <name>] [--memory <bytes>] [--cpu <milli>] [--reserve] [--shared] <id>

Update a runtime by id, including builtin small/medium/large.
--reserve guarantees CPU/memory; --shared turns the guarantee off (default for new runtimes).
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
)

func init() {
	Register("runtime", runRuntimeProfileList, runtimeListUsage)
	Register("runtime:create", runRuntimeProfileCreate, runtimeCreateUsage)
	Register("runtime:update", runRuntimeProfileUpdate, runtimeUpdateUsage)
	Register("runtime:remove", runRuntimeProfileRemove, runtimeRemoveUsage)
	Register("runtime:allow-custom", runRuntimeProfileAllowCustom, runtimeAllowCustomUsage)
	Register("runtime:reserve", runRuntimeReserve, runtimeReserveUsage)
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
	fmt.Printf("allow_custom_limits=%t max_processes=%d\n", settings.AllowCustomLimits, settings.MaxProcessesOrDefault())
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tMEMORY\tCPU\tRESERVE\tBUILTIN")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%t\t%t\n", p.ID, p.Name, resource.FormatLimit(resource.TypeMemory, p.Memory), p.CPU, p.ReserveResources, p.Builtin)
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
	s := &ct.RuntimeSettings{
		AllowCustomLimits: !args.Bool["--disable"],
		MaxProcesses:      cur.MaxProcessesOrDefault(),
		ReserveResources:  cur.ReserveResources,
	}
	if err := client.UpdateRuntimeSettings(s); err != nil {
		return err
	}
	fmt.Printf("allow_custom_limits=%t max_processes=%d\n", s.AllowCustomLimits, s.MaxProcessesOrDefault())
	return nil
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

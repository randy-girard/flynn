package cli

import (
	"fmt"
	"os"
	"strconv"
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
usage: flynn-host runtime:create [--memory <bytes>] [--cpu <milli>] <name>

Create a runtime. Memory is bytes (or 512MB / 1GB). CPU is milliCPU.

Options:
    --memory=<bytes>  Memory cap (also reserved when cluster reservation is on; default 512MB)
    --cpu=<milli>     milliCPU cap (also reserved when cluster reservation is on; default 500)

Examples:

    $ flynn-host runtime:create --memory 512MB --cpu 500 xlarge
`
	runtimeUpdateUsage = `
usage: flynn-host runtime:update [--name <name>] [--memory <bytes>] [--cpu <milli>] <id>

Update a runtime by id, including builtin small/medium/large.
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
usage: flynn-host runtime:reserve [--disable]

Guarantee CPU and memory on the host when placing processes (off by default).
When enabled, a process stays pending until a host has that much free Request.
`
)

func init() {
	Register("runtime", runRuntimeProfileList, runtimeListUsage)
	Register("runtime:create", runRuntimeProfileCreate, runtimeCreateUsage)
	Register("runtime:update", runRuntimeProfileUpdate, runtimeUpdateUsage)
	Register("runtime:remove", runRuntimeProfileRemove, runtimeRemoveUsage)
	Register("runtime:allow-custom", runRuntimeProfileAllowCustom, runtimeAllowCustomUsage)
	Register("runtime:reserve", runRuntimeReserve, runtimeReserveUsage)
	Register("runtime-profile", runRuntimeProfileList, aliasUsage("runtime", "runtime-profile", runtimeListUsage))
	Register("runtime-profile:create", runRuntimeProfileCreate, aliasUsage("runtime:create", "runtime-profile:create", runtimeCreateUsage))
	Register("runtime-profile:update", runRuntimeProfileUpdate, aliasUsage("runtime:update", "runtime-profile:update", runtimeUpdateUsage))
	Register("runtime-profile:remove", runRuntimeProfileRemove, aliasUsage("runtime:remove", "runtime-profile:remove", runtimeRemoveUsage))
	Register("runtime-profile:allow-custom", runRuntimeProfileAllowCustom, aliasUsage("runtime:allow-custom", "runtime-profile:allow-custom", runtimeAllowCustomUsage))
	Register("runtime-profile:reserve", runRuntimeReserve, aliasUsage("runtime:reserve", "runtime-profile:reserve", runtimeReserveUsage))
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
	fmt.Printf("allow_custom_limits=%t max_processes=%d reserve_resources=%t\n", settings.AllowCustomLimits, settings.MaxProcessesOrDefault(), settings.ReserveResources)
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tMEMORY\tCPU\tBUILTIN")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%t\n", p.ID, p.Name, resource.FormatLimit(resource.TypeMemory, p.Memory), p.CPU, p.Builtin)
	}
	return w.Flush()
}

func runRuntimeProfileCreate(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	mem := int64(512 * 1024 * 1024)
	cpu := int64(500)
	if s := args.String["--memory"]; s != "" {
		mem, err = resource.ParseLimit(resource.TypeMemory, s)
		if err != nil {
			return err
		}
	}
	if s := args.String["--cpu"]; s != "" {
		cpu, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			cpu, err = resource.ParseLimit(resource.TypeCPU, s)
			if err != nil {
				return err
			}
		}
	}
	p := &ct.RuntimeProfile{Name: args.String["<name>"], Memory: mem, CPU: cpu}
	if err := client.CreateRuntimeProfile(p); err != nil {
		return err
	}
	fmt.Println(p.ID)
	return nil
}

func runRuntimeProfileUpdate(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	p, err := client.GetRuntimeProfile(args.String["<id>"])
	if err != nil {
		return err
	}
	if s := args.String["--name"]; s != "" {
		p.Name = s
	}
	if s := args.String["--memory"]; s != "" {
		p.Memory, err = resource.ParseLimit(resource.TypeMemory, s)
		if err != nil {
			return err
		}
	}
	if s := args.String["--cpu"]; s != "" {
		p.CPU, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			p.CPU, err = resource.ParseLimit(resource.TypeCPU, s)
			if err != nil {
				return err
			}
		}
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
	fmt.Printf("allow_custom_limits=%t max_processes=%d reserve_resources=%t\n", s.AllowCustomLimits, s.MaxProcessesOrDefault(), s.ReserveResources)
	return nil
}

func runRuntimeReserve(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cur, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	s := &ct.RuntimeSettings{
		AllowCustomLimits: cur.AllowCustomLimits,
		MaxProcesses:      cur.MaxProcessesOrDefault(),
		ReserveResources:  !args.Bool["--disable"],
	}
	if err := client.UpdateRuntimeSettings(s); err != nil {
		return err
	}
	fmt.Printf("allow_custom_limits=%t max_processes=%d reserve_resources=%t\n", s.AllowCustomLimits, s.MaxProcessesOrDefault(), s.ReserveResources)
	return nil
}

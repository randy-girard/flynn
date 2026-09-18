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

func init() {
	Register("runtime-profile", runRuntimeProfileList, `
usage: flynn-host runtime-profile

List cluster runtime environments (CPU/memory presets).
`)
	Register("runtime-profile:create", runRuntimeProfileCreate, `
usage: flynn-host runtime-profile:create [--memory <bytes>] [--cpu <milli>] <name>

Create a runtime environment. Memory is bytes (or 512MB / 1GB). CPU is milliCPU.

Options:
    --memory=<bytes>  Memory limit (default 512MB)
    --cpu=<milli>     milliCPU limit (default 500)

Examples:

    $ flynn-host runtime-profile:create --memory 512MB --cpu 500 xlarge
`)
	Register("runtime-profile:update", runRuntimeProfileUpdate, `
usage: flynn-host runtime-profile:update [--name <name>] [--memory <bytes>] [--cpu <milli>] <id>

Update a runtime environment by id, including builtin small/medium/large.
`)
	Register("runtime-profile:remove", runRuntimeProfileRemove, `
usage: flynn-host runtime-profile:remove <id>

Delete a custom runtime environment. Builtin small/medium/large cannot be removed.
`)
	Register("runtime-profile:allow-custom", runRuntimeProfileAllowCustom, `
usage: flynn-host runtime-profile:allow-custom [--disable]

Allow (or --disable) cluster admins and app operators to set raw CPU/memory
limits instead of only named profiles.
`)
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
	fmt.Printf("allow_custom_limits=%t\n", settings.AllowCustomLimits)
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
	s := &ct.RuntimeSettings{AllowCustomLimits: !args.Bool["--disable"]}
	if err := client.UpdateRuntimeSettings(s); err != nil {
		return err
	}
	fmt.Printf("allow_custom_limits=%t\n", s.AllowCustomLimits)
	return nil
}

package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dbruntime"
)

const (
	dbRuntimeListUsage = `
usage: flynn-host db-runtime

List database runtimes (CPU, memory, and disk per engine).

These are not app process runtimes. App processes use flynn-host runtime
and flynn limit:runtime. Database runtimes size a new resource:add instance.

Presets live in memory. Creates, updates, and the custom-size flag are
stored in /etc/flynn/db-runtimes.json (override with FLYNN_DB_RUNTIMES).
A missing file uses builtin small, medium, and large. Changing a definition
does not resize instances already created from it. There is no in-place resize.
`
	dbRuntimeCreateUsage = `
usage: flynn-host db-runtime:create --memory <bytes> --cpu <milli> --disk <bytes> <engine> <name>

Create a database runtime for one engine (postgres, redis, mariadb, mongodb, kafka, clickhouse).
mysql is accepted as mariadb. Memory and disk are bytes or a size like 512MB or 10GB.
CPU is milliCPU.

Options:
    --memory=<bytes>  memory cap
    --cpu=<milli>     milliCPU cap
    --disk=<bytes>    disk cap

Examples:

    $ flynn-host db-runtime:create --memory 1GB --cpu 500 --disk 20GB redis cache
`
	dbRuntimeUpdateUsage = `
usage: flynn-host db-runtime:update [--rename <new-name>] [--memory <bytes>] [--cpu <milli>] [--disk <bytes>] <engine> <name>

Update a database runtime. Builtin small, medium, and large can change
CPU, memory, and disk. Existing instances keep the size they were created with.

Options:
    --rename=<new-name>  new name for a custom runtime
    --memory=<bytes>     memory cap
    --cpu=<milli>        milliCPU cap
    --disk=<bytes>       disk cap
`
	dbRuntimeRemoveUsage = `
usage: flynn-host db-runtime:remove <engine> <name>

Remove a custom database runtime. Builtin small, medium, and large cannot be removed.
`
	dbRuntimeAllowCustomUsage = `
usage: flynn-host db-runtime:allow-custom [--disable]

Allow tenants to set raw CPU, memory, and disk on flynn resource:add.
Without this, resource:add only accepts a published runtime name (default small).
--disable turns custom sizes back off.
`
)

func init() {
	Register("db-runtime", runDBRuntimeList, dbRuntimeListUsage)
	Register("db-runtime:create", runDBRuntimeCreate, dbRuntimeCreateUsage)
	Register("db-runtime:update", runDBRuntimeUpdate, dbRuntimeUpdateUsage)
	Register("db-runtime:remove", runDBRuntimeRemove, dbRuntimeRemoveUsage)
	Register("db-runtime:allow-custom", runDBRuntimeAllowCustom, dbRuntimeAllowCustomUsage)
}

func dbRuntimeCatalog() (dbruntime.Catalog, string, error) {
	path := dbruntime.Path()
	cat, err := dbruntime.Load(path)
	return cat, path, err
}

func runDBRuntimeList(_ *docopt.Args) error {
	cat, path, err := dbRuntimeCatalog()
	if err != nil {
		return err
	}
	fmt.Printf("allow_custom_sizes=%t file=%s\n", cat.AllowCustomSizes, path)
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ENGINE\tNAME\tCPU\tMEMORY\tDISK\tBUILTIN")
	for _, r := range cat.List() {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%t\n",
			r.Engine, r.Name, r.CPU,
			resource.FormatLimit(resource.TypeMemory, r.Memory),
			resource.FormatLimit(resource.TypeTempDisk, r.Disk),
			r.Builtin)
	}
	return w.Flush()
}

func runDBRuntimeCreate(args *docopt.Args) error {
	cpu, err := dbruntime.ParseCPU(args.String["--cpu"])
	if err != nil {
		return err
	}
	mem, err := dbruntime.ParseBytes(args.String["--memory"])
	if err != nil {
		return err
	}
	disk, err := dbruntime.ParseBytes(args.String["--disk"])
	if err != nil {
		return err
	}
	cat, path, err := dbRuntimeCatalog()
	if err != nil {
		return err
	}
	r := dbruntime.Runtime{
		Name:   strings.ToLower(strings.TrimSpace(args.String["<name>"])),
		Engine: args.String["<engine>"],
		CPU:    cpu,
		Memory: mem,
		Disk:   disk,
	}
	if err := cat.Create(r); err != nil {
		return err
	}
	if err := dbruntime.Save(path, cat); err != nil {
		return err
	}
	publishDBRuntimes(cat)
	eng, err := dbruntime.NormalizeEngine(r.Engine)
	if err != nil {
		return err
	}
	fmt.Printf("%s/%s\n", eng, r.Name)
	return nil
}

func runDBRuntimeUpdate(args *docopt.Args) error {
	cat, path, err := dbRuntimeCatalog()
	if err != nil {
		return err
	}
	var fields dbruntime.UpdateFields
	if s := args.String["--rename"]; s != "" {
		fields.Name = &s
	}
	if s := args.String["--cpu"]; s != "" {
		n, err := dbruntime.ParseCPU(s)
		if err != nil {
			return err
		}
		fields.CPU = &n
	}
	if s := args.String["--memory"]; s != "" {
		n, err := dbruntime.ParseBytes(s)
		if err != nil {
			return err
		}
		fields.Memory = &n
	}
	if s := args.String["--disk"]; s != "" {
		n, err := dbruntime.ParseBytes(s)
		if err != nil {
			return err
		}
		fields.Disk = &n
	}
	r, err := cat.Update(args.String["<engine>"], args.String["<name>"], fields)
	if err != nil {
		return err
	}
	if err := dbruntime.Save(path, cat); err != nil {
		return err
	}
	publishDBRuntimes(cat)
	fmt.Printf("%s/%s\n", r.Engine, r.Name)
	return nil
}

func runDBRuntimeRemove(args *docopt.Args) error {
	cat, path, err := dbRuntimeCatalog()
	if err != nil {
		return err
	}
	if err := cat.Remove(args.String["<engine>"], args.String["<name>"]); err != nil {
		return err
	}
	if err := dbruntime.Save(path, cat); err != nil {
		return err
	}
	publishDBRuntimes(cat)
	return nil
}

func runDBRuntimeAllowCustom(args *docopt.Args) error {
	cat, path, err := dbRuntimeCatalog()
	if err != nil {
		return err
	}
	cat.AllowCustomSizes = !args.Bool["--disable"]
	if err := dbruntime.Save(path, cat); err != nil {
		return err
	}
	publishDBRuntimes(cat)
	fmt.Printf("allow_custom_sizes=%t\n", cat.AllowCustomSizes)
	return nil
}

// publishDBRuntimes copies the host catalog to the controller. flynn-host is
// on the cluster and uses the controller key, so this is allowed. Plugin jobs
// started by flynn-host use that same key against POST /db-runtimes. A missing
// controller (unit tests, a host that is not bootstrapped) keeps the file.
func publishDBRuntimes(cat dbruntime.Catalog) {
	client, err := controllerClient()
	if err != nil {
		return
	}
	type publisher interface {
		ReplaceDBRuntimes(catalog *dbruntime.Catalog) error
	}
	api, ok := client.(publisher)
	if !ok {
		return
	}
	_ = api.ReplaceDBRuntimes(&cat)
}

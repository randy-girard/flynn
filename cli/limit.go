package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

func init() {
	register("limit", runLimitList, `
usage: flynn limit [-t <proc>]

List app resource limits.

Options:
	-t, --process-type=<proc>  read limits for specified process type

Examples:

	$ flynn limit
	web:     cpu=1000  temp_disk=100MB  max_fd=10000  memory=1GB
	worker:  cpu=1000  temp_disk=100MB  max_fd=10000  memory=1GB
`)
	register("limit:profiles", runLimitProfiles, `
usage: flynn limit:profiles

List cluster runtime environments (small, medium, large, and custom profiles).
Apply one to a process type with flynn limit:profile.

Examples:

	$ flynn limit:profiles
	NAME    MEMORY  CPU   BUILTIN
	small   256MB   250   true
	medium  1GB     1000  true
	large   2GB     2000  true
`)
	register("limit:set", runLimitSet, `
usage: flynn limit:set <proc> <var>=<val>...

Set app resource limits.

Examples:

	$ flynn limit:set web memory=512MB max_fd=12000 cpu=500 temp_disk=200MB
	Created release 5058ae7964f74c399a240bdd6e7d1bcb
`)
	register("limit:profile", runLimitProfile, `
usage: flynn limit:profile <proc> <profile>

Apply a named runtime environment (small, medium, large, or a custom profile)
to a process type. Custom CPU/memory numbers require the cluster setting
allow_custom_limits (see flynn-host runtime-profile:allow-custom).

Examples:

	$ flynn limit:profile web small
	Created release 5058ae7964f74c399a240bdd6e7d1bcb
`)
}

func runLimitList(args *docopt.Args, client controller.Client) error {

	release, err := client.GetAppRelease(mustApp())
	if err == controller.ErrNotFound {
		return nil
	} else if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	if procType := args.String["--process-type"]; procType != "" {
		t, ok := release.Processes[procType]
		if !ok {
			return fmt.Errorf("unknown process type %q", procType)
		}
		formatLimits(w, procType, t)
		return nil
	}

	for s, t := range release.Processes {
		formatLimits(w, s, t)
	}
	return nil
}

func formatLimits(w io.Writer, s string, t ct.ProcessType) {
	r := t.Resources
	limits := make([]string, 0, len(r)+1)
	if t.RuntimeProfile != "" {
		limits = append(limits, "profile="+t.RuntimeProfile)
	}
	for typ, spec := range r {
		if limit := spec.Limit; limit != nil {
			limits = append(limits, fmt.Sprintf("%s=%s", typ, resource.FormatLimit(typ, *limit)))
		}
	}
	sort.Strings(limits)
	fmt.Fprintf(w, "%s:\t%s\n", s, strings.Join(limits, "\t"))
}

func runLimitSet(args *docopt.Args, client controller.Client) error {
	proc := args.String["<proc>"]
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease(app.ID)
	if err == controller.ErrNotFound {
		release = &ct.Release{}
	} else if err != nil {
		return err
	}

	if release.Processes == nil {
		release.Processes = make(map[string]ct.ProcessType)
	}
	t, ok := release.Processes[proc]
	if !ok && proc != "slugbuilder" {
		fmt.Fprintf(os.Stderr, "Warning: %q is not an existing process type, setting anyway\n", proc)
	}
	if t.Resources == nil {
		t.Resources = resource.Defaults()
	}

	settings, err := client.GetRuntimeSettings()
	if err != nil {
		return err
	}
	if !settings.AllowCustomLimits {
		return fmt.Errorf("custom CPU/memory limits are disabled on this cluster; apply a named profile with `flynn limit:profile %s <small|medium|large>` or ask a cluster admin to enable custom limits", proc)
	}

	resources, err := resource.Parse(cliutil.List(args, "<var>=<val>"))
	if err != nil {
		return err
	}
	for typ, limit := range resources {
		t.Resources[typ] = limit
	}
	t.RuntimeProfile = ""
	release.Processes[proc] = t

	release.ID = ""
	if err := client.CreateRelease(app.ID, release); err != nil {
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return err
	}
	fmt.Printf("Created release %s\n", release.ID)
	return nil
}

func runLimitProfiles(_ *docopt.Args, client controller.Client) error {
	list, err := client.ListRuntimeProfiles()
	if err != nil {
		return err
	}
	fmt.Print(formatRuntimeProfiles(list))
	return nil
}

func runLimitProfile(args *docopt.Args, client controller.Client) error {
	proc := args.String["<proc>"]
	name := args.String["<profile>"]
	list, err := client.ListRuntimeProfiles()
	if err != nil {
		return err
	}
	profile := lookupRuntimeProfile(list, name)
	if profile == nil {
		return fmt.Errorf("unknown runtime profile %q; run `flynn limit:profiles`", name)
	}
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease(app.ID)
	if err == controller.ErrNotFound {
		release = &ct.Release{}
	} else if err != nil {
		return err
	}
	if release.Processes == nil {
		release.Processes = make(map[string]ct.ProcessType)
	}
	t := applyRuntimeProfileToProc(release.Processes[proc], profile)
	if _, ok := release.Processes[proc]; !ok && proc != "slugbuilder" {
		fmt.Fprintf(os.Stderr, "Warning: %q is not an existing process type, setting anyway\n", proc)
	}
	release.Processes[proc] = t
	release.ID = ""
	if err := client.CreateRelease(app.ID, release); err != nil {
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return err
	}
	fmt.Printf("Created release %s (%s: profile=%s memory=%s cpu=%d)\n",
		release.ID, proc, profile.Name,
		resource.FormatLimit(resource.TypeMemory, profile.Memory), profile.CPU)
	return nil
}

func lookupRuntimeProfile(list []*ct.RuntimeProfile, nameOrID string) *ct.RuntimeProfile {
	want := strings.ToLower(strings.TrimSpace(nameOrID))
	if want == "" {
		return nil
	}
	for _, p := range list {
		if p == nil {
			continue
		}
		if strings.ToLower(p.Name) == want || strings.ToLower(p.ID) == want {
			return p
		}
	}
	return nil
}

func applyRuntimeProfileToProc(t ct.ProcessType, profile *ct.RuntimeProfile) ct.ProcessType {
	if t.Resources == nil {
		t.Resources = resource.Defaults()
	}
	resource.ApplyNamedLimits(t.Resources, profile.Memory, profile.CPU)
	t.RuntimeProfile = profile.Name
	return t
}

func formatRuntimeProfiles(list []*ct.RuntimeProfile) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMEMORY\tCPU\tBUILTIN")
	sorted := append([]*ct.RuntimeProfile{}, list...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i] == nil || sorted[j] == nil {
			return sorted[j] != nil
		}
		return sorted[i].Name < sorted[j].Name
	})
	for _, p := range sorted {
		if p == nil {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%t\n", p.Name, resource.FormatLimit(resource.TypeMemory, p.Memory), p.CPU, p.Builtin)
	}
	w.Flush()
	return b.String()
}

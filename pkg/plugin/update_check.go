package plugin

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
)

const (
	UpdateStatusCurrent = "current"
	UpdateStatusUpdate  = "update"
	UpdateStatusUnknown = "-"
	UpdateStatusError   = "error"
)

// ListedPlugin is one installed plugin for plugin:list, optionally with a
// GitHub update check against this Flynn version.
type ListedPlugin struct {
	Installed
	Available string
	Status    string
	Err       error
}

func dash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func (p ListedPlugin) Version() string {
	return dash(p.Ref)
}

func (p ListedPlugin) UpdateTag() string {
	return dash(p.Available)
}

func (p ListedPlugin) StatusOrDash() string {
	if p.Status == "" {
		return UpdateStatusUnknown
	}
	return p.Status
}

// GitHubSourceForInstalled picks a GitHub repo for an update check: stamped
// github_repo, official catalog, then a git Source URL.
func GitHubSourceForInstalled(rec Installed, githubOrg string) *GitHubSource {
	org := strings.TrimSpace(githubOrg)
	if org == "" {
		org = DefaultGitHubOrg()
	}
	try := func(raw string) *GitHubSource {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		if !looksLikeGitURL(raw) {
			raw = strings.TrimSuffix(raw, ".git")
			if !strings.Contains(raw, "/") {
				raw = org + "/" + raw
			}
			raw = "https://github.com/" + raw + ".git"
		}
		gh, err := ParseGitHubURL(raw)
		if err != nil {
			return nil
		}
		return gh
	}
	if gh := try(rec.GitHubRepo); gh != nil {
		return gh
	}
	if off := LookupOfficial(rec); off != nil {
		if gh := try(off.RepoSlug(org)); gh != nil {
			return gh
		}
	}
	if looksLikeGitURL(rec.Source) {
		return try(rec.Source)
	}
	if rec.Name != "" && !strings.ContainsAny(rec.Name, `/\`) && !IsPrivatePluginName(rec.Name) {
		return try("flynn-plugin-" + rec.Name)
	}
	return nil
}

// ListedFromInstalled wraps inventory rows for plugin:list without a GitHub check.
func ListedFromInstalled(plugins []Installed) []ListedPlugin {
	out := make([]ListedPlugin, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, ListedPlugin{Installed: p, Status: UpdateStatusUnknown})
	}
	return out
}

// CheckUpdates looks up the highest compatible GitHub tag for each plugin.
func (in *Installer) CheckUpdates(plugins []Installed, credsFile string) []ListedPlugin {
	if in == nil {
		in = &Installer{}
	}
	out := make([]ListedPlugin, len(plugins))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, p := range plugins {
		wg.Add(1)
		go func(i int, p Installed) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = in.checkOneUpdate(p, credsFile)
		}(i, p)
	}
	wg.Wait()
	return out
}

func (in *Installer) checkOneUpdate(p Installed, credsFile string) ListedPlugin {
	row := ListedPlugin{Installed: p, Status: UpdateStatusUnknown}
	src := GitHubSourceForInstalled(p, "")
	if src == nil {
		return row
	}
	token, api, err := TokenForHost(src.Host, credsFile)
	if err != nil {
		row.Status = UpdateStatusError
		row.Err = err
		return row
	}
	if api != "" {
		src.API = api
	}
	if src.API == "" {
		src.API = "https://api.github.com"
	}
	rel, err := in.latestCalVerRelease(src, token)
	if err != nil {
		row.Status = UpdateStatusError
		row.Err = err
		return row
	}
	row.Available = rel.TagName
	if PluginNeedsUpdate(p.Ref, row.Available) {
		row.Status = UpdateStatusUpdate
	} else {
		row.Status = UpdateStatusCurrent
	}
	return row
}

// WriteHostPluginTable is flynn-host plugin:list output.
func WriteHostPluginTable(w io.Writer, rows []ListedPlugin, check bool) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	if check {
		fmt.Fprintln(tw, "NAME\tKIND\tCLI\tSOURCE\tVERSION\tUPDATE\tSTATUS")
	} else {
		fmt.Fprintln(tw, "NAME\tKIND\tCLI\tSOURCE\tVERSION")
	}
	for _, p := range rows {
		cliName := ""
		if p.CLI != nil {
			cliName = p.CLI.Command
		}
		if check {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Kind, cliName, dash(p.Source), p.Version(), p.UpdateTag(), p.StatusOrDash())
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Kind, cliName, dash(p.Source), p.Version())
		}
	}
	return tw.Flush()
}

// WriteUserPluginTable is flynn plugin:list output.
func WriteUserPluginTable(w io.Writer, rows []ListedPlugin, check bool) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	if check {
		fmt.Fprintln(tw, "NAME\tKIND\tCOMMAND\tUSAGE\tVERSION\tUPDATE\tSTATUS")
	} else {
		fmt.Fprintln(tw, "NAME\tKIND\tCOMMAND\tUSAGE\tVERSION")
	}
	for _, p := range rows {
		cmd, usage := "", ""
		if p.CLI != nil && p.CLI.UserVisible(p.Kind) {
			cmd = p.CLI.Command
			usage = p.CLI.Usage
		}
		if check {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Kind, cmd, usage, p.Version(), p.UpdateTag(), p.StatusOrDash())
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Kind, cmd, usage, p.Version())
		}
	}
	return tw.Flush()
}

func CountPluginUpdates(rows []ListedPlugin) int {
	n := 0
	for _, p := range rows {
		if p.Status == UpdateStatusUpdate {
			n++
		}
	}
	return n
}

func WritePluginUpdateNotes(w io.Writer, rows []ListedPlugin) {
	if w == nil {
		return
	}
	for _, p := range rows {
		if p.Err != nil {
			fmt.Fprintf(w, "%s: %s\n", p.Name, p.Err)
		}
	}
	if n := CountPluginUpdates(rows); n > 0 {
		fmt.Fprintf(w, "%d plugin(s) can be updated. Run flynn-host plugin:update <name> or flynn-host plugin:update-all.\n", n)
	}
}

// Package clihelp formats flynn / flynn-host command help. Root help lists
// parent commands; help for a parent lists its immediate children.
package clihelp

import (
	"fmt"
	"sort"
	"strings"
)

// Item is one command row in a help list.
type Item struct {
	Name string
	Desc string
}

// StripHelp returns args without -h / --help. "--" stops scanning.
func StripHelp(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		if a == "-h" || a == "--help" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Parents returns unique first-segment names (env from env:set, plugin from
// plugin:list). Names are sorted.
func Parents(all []string) []string {
	seen := make(map[string]struct{}, len(all))
	var out []string
	for _, n := range all {
		parent := n
		if i := strings.IndexByte(n, ':'); i >= 0 {
			parent = n[:i]
		}
		if _, ok := seen[parent]; ok {
			continue
		}
		seen[parent] = struct{}{}
		out = append(out, parent)
	}
	sort.Strings(out)
	return out
}

// HasChildren reports whether any name is parent plus a colon suffix.
func HasChildren(all []string, parent string) bool {
	if parent == "" {
		return false
	}
	prefix := parent + ":"
	for _, n := range all {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// Children returns immediate child verbs under parent (the part after
// parent:). Grandchildren are omitted when an intermediate command exists
// (plugin:credentials:set is skipped if plugin:credentials is registered).
func Children(all []string, parent string) []string {
	if parent == "" {
		return nil
	}
	prefix := parent + ":"
	set := make(map[string]struct{}, len(all))
	for _, n := range all {
		set[n] = struct{}{}
	}
	var out []string
	for _, n := range all {
		rest, ok := strings.CutPrefix(n, prefix)
		if !ok || rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, ':'); i >= 0 {
			if _, ok := set[prefix+rest[:i]]; ok {
				continue
			}
		}
		out = append(out, rest)
	}
	sort.Strings(out)
	return out
}

// ShortDescription is the first prose line of a docopt usage string.
func ShortDescription(usage string) string {
	for _, line := range strings.Split(usage, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "usage:"),
			strings.HasPrefix(lower, "options:"),
			strings.HasPrefix(lower, "example:"),
			strings.HasPrefix(lower, "examples:"),
			strings.HasPrefix(lower, "commands:"),
			strings.HasPrefix(line, "flynn-host "),
			strings.HasPrefix(line, "flynn "),
			strings.HasPrefix(line, "-"),
			strings.HasPrefix(line, "$"):
			continue
		}
		if i := strings.IndexByte(line, '.'); i > 0 {
			line = line[:i]
		}
		return line
	}
	return ""
}

// FormatItems renders an aligned two-column command list.
func FormatItems(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	width := 8
	for _, it := range items {
		if n := len(it.Name); n > width {
			width = n
		}
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, it.Name, it.Desc)
	}
	return b.String()
}

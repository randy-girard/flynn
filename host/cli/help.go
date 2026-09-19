package cli

import (
	"fmt"
	"sort"
	"strings"
)

type namespaceCmd struct {
	verb string
	desc string
}

// WantsHelp reports whether args request command help (-h / --help).
func WantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func helpOnlyArgs(args []string) bool {
	for _, a := range args {
		if a != "-h" && a != "--help" {
			return false
		}
	}
	return true
}

// FormatHelp is the text printed for `flynn-host <command> --help`. Namespace
// roots also list sibling commands with a one-line description.
func FormatHelp(name string) string {
	var b strings.Builder
	if cmd := commands[name]; cmd != nil {
		b.WriteString(strings.TrimRight(cmd.usage, "\n"))
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "usage: flynn-host %s\n", name)
	}
	sibs := namespaceCommands(name)
	if len(sibs) == 0 || strings.Contains(b.String(), "\nCommands:") {
		return b.String()
	}
	b.WriteString("\nCommands:\n")
	width := 0
	for _, s := range sibs {
		if n := len(s.verb); n > width {
			width = n
		}
	}
	if width < 8 {
		width = 8
	}
	for _, s := range sibs {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, s.verb, s.desc)
	}
	return b.String()
}

func namespaceCommands(name string) []namespaceCmd {
	prefix := siblingPrefix(name)
	if prefix == "" {
		return nil
	}
	var out []namespaceCmd
	for cmdName, cmd := range commands {
		if _, aliased := hyphenAliases[cmdName]; aliased {
			continue
		}
		rest, ok := strings.CutPrefix(cmdName, prefix)
		if !ok || rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, ':'); i >= 0 {
			if commands[prefix+rest[:i]] != nil {
				continue
			}
		}
		out = append(out, namespaceCmd{
			verb: rest,
			desc: shortDescription(cmd.usage),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].verb < out[j].verb })
	return out
}

func siblingPrefix(name string) string {
	if hasPrefixedCommands(name + ":") {
		return name + ":"
	}
	if !strings.Contains(name, ":") {
		return ""
	}
	for alias, target := range topAliases {
		if target == name && hasPrefixedCommands(alias+":") {
			return alias + ":"
		}
	}
	return ""
}

func hasPrefixedCommands(prefix string) bool {
	for name := range commands {
		if strings.HasPrefix(name, prefix) && name != strings.TrimSuffix(prefix, ":") {
			return true
		}
	}
	return false
}

func shortDescription(usage string) string {
	for _, line := range strings.Split(usage, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "usage:"),
			strings.HasPrefix(lower, "options:"),
			strings.HasPrefix(lower, "examples:"),
			strings.HasPrefix(lower, "commands:"),
			strings.HasPrefix(line, "flynn-host "),
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

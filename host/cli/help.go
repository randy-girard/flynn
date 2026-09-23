package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/pkg/clihelp"
)

const rootHelpHeader = `usage: flynn-host [-h|--help] [--version] [<command>] [<args>...]

Options:
  -h, --help                 Show this message
  --version                  Show current version
`

const rootHelpFooter = `
See 'flynn-host help <command>' or 'flynn-host <command> --help' for a command and its subcommands.
`

type namespaceCmd struct {
	name string
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

func registeredHelpNames() []string {
	names := make([]string, 0, len(commands))
	for n := range commands {
		if _, aliased := hyphenAliases[n]; aliased {
			continue
		}
		names = append(names, n)
	}
	return names
}

// HelpTopic is the command whose help to print for `flynn-host help …` or
// `flynn-host <command> --help`. Namespace roots keep their name so children
// are listed; aliases such as create→apps:create still resolve.
func HelpTopic(name string, args []string) string {
	if name == "" {
		return name
	}
	args = clihelp.StripHelp(args)
	all := registeredHelpNames()
	if clihelp.HasChildren(all, name) && len(args) == 0 {
		return name
	}
	resolved, _, _ := ResolveCommand(name, args)
	return resolved
}

// KnownHelpTopic reports whether FormatHelp has something to show.
func KnownHelpTopic(name string) bool {
	if name == "" {
		return false
	}
	if commands[name] != nil {
		return true
	}
	if _, ok := hyphenAliases[name]; ok {
		return true
	}
	return clihelp.HasChildren(registeredHelpNames(), name)
}

// RootHelp is printed for `flynn-host`, `flynn-host help`, and `flynn-host --help`.
func RootHelp() string {
	all := registeredHelpNames()
	parents := clihelp.Parents(all)
	items := make([]clihelp.Item, 0, len(parents)+1)
	seen := map[string]struct{}{}
	add := func(name, desc string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		if desc == "" {
			desc = name + " commands"
		}
		items = append(items, clihelp.Item{Name: name, Desc: desc})
	}
	add("help", "Show usage for a specific command")
	for _, name := range parents {
		add(name, clihelp.ShortDescription(commandUsage(name)))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return rootHelpHeader + "\nCommands:\n" + clihelp.FormatItems(items) + rootHelpFooter
}

func commandUsage(name string) string {
	if cmd := commands[name]; cmd != nil {
		return cmd.usage
	}
	if target, ok := topAliases[name]; ok {
		if cmd := commands[target]; cmd != nil {
			return cmd.usage
		}
	}
	return ""
}

// FormatHelp is the text printed for `flynn-host <command> --help`. Parents
// list immediate children with a one-line description.
func FormatHelp(name string) string {
	if target, ok := hyphenAliases[name]; ok {
		name = target
	}
	var b strings.Builder
	if cmd := commands[name]; cmd != nil {
		b.WriteString(strings.TrimRight(cmd.usage, "\n"))
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "usage: flynn-host %s\n", name)
		if clihelp.HasChildren(registeredHelpNames(), name) {
			fmt.Fprintf(&b, "       flynn-host %s <command> [<args>...]\n", name)
			b.WriteByte('\n')
		}
	}
	children := namespaceCommands(name)
	if len(children) == 0 || strings.Contains(b.String(), "\nCommands:") {
		return b.String()
	}
	b.WriteString("\nCommands:\n")
	items := make([]clihelp.Item, 0, len(children))
	for _, s := range children {
		items = append(items, clihelp.Item{Name: s.name, Desc: s.desc})
	}
	b.WriteString(clihelp.FormatItems(items))
	return b.String()
}

func namespaceCommands(name string) []namespaceCmd {
	all := registeredHelpNames()
	verbs := clihelp.Children(all, name)
	out := make([]namespaceCmd, 0, len(verbs))
	for _, verb := range verbs {
		full := name + ":" + verb
		out = append(out, namespaceCmd{
			name: full,
			desc: clihelp.ShortDescription(commandUsage(full)),
		})
	}
	return out
}

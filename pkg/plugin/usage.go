package plugin

import (
	"strings"
)

// DocoptUsage is the plugin cli.doc with colon form listed first and space
// form as a fallback. Flynn expands `flynn pg:psql` to argv ["pg", "psql"]
// before docopt, so both patterns must exist for parse and --help.
func (c *CLI) DocoptUsage() string {
	if c == nil {
		return ""
	}
	return ExpandCLIUsage(c)
}

// ExpandCLIUsage rewrites a plugin usage block so each command pattern exists
// in colon form (canonical) and space form (fallback). Existing lines that
// are not this plugin's command (for example `flynn resource:add`) are kept.
func ExpandCLIUsage(c *CLI) string {
	if c == nil || strings.TrimSpace(c.Doc) == "" {
		return ""
	}
	usage, rest, ok := cutUsageBlock(c.Doc)
	if !ok {
		return c.Doc
	}
	var colon, space []string
	seenColon := map[string]bool{}
	seenSpace := map[string]bool{}
	add := func(s string, dest *[]string, seen map[string]bool) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		*dest = append(*dest, s)
	}
	for _, p := range usagePatterns(usage) {
		cf, sf := usageForms(c, p)
		add(cf, &colon, seenColon)
		add(sf, &space, seenSpace)
	}
	if len(colon) == 0 {
		return c.Doc
	}
	var b strings.Builder
	for i, p := range colon {
		if i == 0 {
			b.WriteString("usage: ")
		} else {
			b.WriteString("       ")
		}
		b.WriteString(p)
		b.WriteByte('\n')
	}
	for _, p := range space {
		if seenColon[p] {
			continue
		}
		b.WriteString("       ")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	b.WriteString(rest)
	return b.String()
}

// FoldColonBools copies canonical `pg:psql` docopt bools onto action tokens
// (`psql`) so MatchAction works whether argv was colon form or space form.
func FoldColonBools(c *CLI, bools map[string]bool) {
	if c == nil || bools == nil {
		return
	}
	for _, a := range c.Actions {
		if !bools[ColonName(c.Command, a.Name)] {
			continue
		}
		for _, p := range strings.Fields(a.Name) {
			bools[p] = true
		}
	}
	for _, alias := range []string{"list", "help"} {
		if bools[c.Command+":"+alias] {
			bools[alias] = true
		}
	}
}

func cutUsageBlock(doc string) (usage, rest string, ok bool) {
	lines := strings.Split(doc, "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(lines[0])), "usage:") {
		return "", doc, false
	}
	i := 1
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" {
			break
		}
		i++
	}
	return strings.Join(lines[:i], "\n"), strings.Join(lines[i:], "\n"), true
}

func usagePatterns(usage string) []string {
	var out []string
	for _, line := range strings.Split(usage, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trim), "usage:") {
			trim = strings.TrimSpace(trim[len("usage:"):])
		}
		if trim != "" {
			out = append(out, trim)
		}
	}
	return out
}

func usageForms(c *CLI, pattern string) (colon, space string) {
	pattern = strings.TrimSpace(pattern)
	command := ""
	if c != nil {
		command = c.Command
	}
	prefix := "flynn " + command
	if command == "" || pattern == prefix {
		return pattern, pattern
	}
	var verbs []string
	var tail string
	switch {
	case strings.HasPrefix(pattern, prefix+":"):
		verbs, tail = splitUsageVerbs(strings.TrimPrefix(pattern, prefix+":"), true)
	case strings.HasPrefix(pattern, prefix+" "):
		verbs, tail = splitUsageVerbs(strings.TrimPrefix(pattern, prefix+" "), false)
	default:
		return pattern, pattern
	}
	return colonPattern(c, verbs, tail), spacePattern(c, verbs, tail)
}

func splitUsageVerbs(rem string, colonJoined bool) (verbs []string, tail string) {
	rem = strings.TrimSpace(rem)
	if rem == "" {
		return nil, ""
	}
	fields := strings.Fields(rem)
	if colonJoined {
		if isUsageTail(fields[0]) {
			return nil, rem
		}
		return strings.Split(fields[0], ":"), strings.Join(fields[1:], " ")
	}
	i := 0
	for i < len(fields) && !isUsageTail(fields[i]) {
		i++
	}
	return fields[:i], strings.Join(fields[i:], " ")
}

func isUsageTail(tok string) bool {
	return tok == "--" || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "[")
}

func colonPattern(c *CLI, verbs []string, tail string) string {
	command := c.Command
	suffix := strings.Join(verbs, ":")
	if len(verbs) > 0 {
		for _, a := range c.Actions {
			if sameStrings(strings.Fields(a.Name), verbs) {
				suffix = strings.TrimPrefix(ColonName(command, a.Name), command+":")
				break
			}
		}
	}
	s := "flynn " + command
	if suffix != "" {
		s += ":" + suffix
	}
	if tail != "" {
		s += " " + tail
	}
	return s
}

func spacePattern(c *CLI, verbs []string, tail string) string {
	command := c.Command
	spaceVerbs := verbs
	colon := command + ":" + strings.Join(verbs, ":")
	for _, a := range c.Actions {
		if ColonName(command, a.Name) == colon || ColonName(command, a.Name) == command+":"+strings.Join(verbs, "-") {
			spaceVerbs = strings.Fields(a.Name)
			break
		}
	}
	s := "flynn " + command
	if len(spaceVerbs) > 0 {
		s += " " + strings.Join(spaceVerbs, " ")
	}
	if tail != "" {
		s += " " + tail
	}
	return s
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
